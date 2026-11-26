// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package integration

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/kube"
	"github.com/bitwise-media-group/patchy/pkg/source"
)

// fakeLister yields canned findings and records its calls.
type fakeLister struct {
	findings []source.Finding
	complete bool
	err      error
	calls    int
	repos    []string
}

func (f *fakeLister) List(
	_ context.Context, repos []string, yield func(source.Finding) bool,
) (bool, error) {
	f.calls++
	f.repos = repos
	if f.err != nil {
		return false, f.err
	}
	for _, fd := range f.findings {
		if !yield(fd) {
			return false, nil
		}
	}
	return f.complete, nil
}

// newBackfillReconciler builds a Reconcile-level harness: a Ready
// Integration, a BackfillRequest on it, a real Ingestor over the fake
// client, and the ListerFor hook.
func newBackfillReconciler(
	t *testing.T, integ *v1alpha1.Integration, bfr *v1alpha1.BackfillRequest, lister *fakeLister, listerOK bool,
) (*BackfillReconciler, client.Client) {
	t.Helper()
	objs := []client.Object{bfr}
	if integ != nil {
		if integ.Status.Conditions == nil {
			integ.Status.Conditions = []metav1.Condition{{
				Type: v1alpha1.ConditionReady, Status: metav1.ConditionTrue, Reason: "CredentialValid",
				LastTransitionTime: metav1.NewTime(testClock),
			}}
		}
		objs = append(objs, integ)
	}
	c := fake.NewClientBuilder().
		WithScheme(kube.Scheme()).
		WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.Finding{}, &v1alpha1.Integration{}, &v1alpha1.BackfillRequest{}).
		WithIndex(&v1alpha1.Finding{}, KeyHashIndex, KeyHashIndexer).
		Build()
	clock := testClock
	r := &BackfillReconciler{
		Client: c,
		Now:    func() time.Time { return clock },
		Ingest: &Ingestor{Client: c, Namespace: "patchy", Window: time.Hour},
		ListerFor: func(context.Context, *v1alpha1.Integration) (source.Lister, bool, error) {
			if !listerOK {
				return nil, false, nil
			}
			return lister, true, nil
		},
	}
	return r, c
}

func backfillRequest(repos ...string) *v1alpha1.BackfillRequest {
	return &v1alpha1.BackfillRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "backfill-1", Namespace: "patchy",
			CreationTimestamp: metav1.NewTime(testClock)},
		Spec: v1alpha1.BackfillRequestSpec{
			IntegrationRef: v1alpha1.LocalObjectReference{Name: "gh"},
			RequestedBy:    "dev",
			Repositories:   repos,
		},
	}
}

func reconcileBackfill(t *testing.T, r *BackfillReconciler) (ctrl.Result, *v1alpha1.BackfillRequest) {
	t.Helper()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "patchy", Name: "backfill-1"}}
	res, err := r.Reconcile(t.Context(), req)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	var bfr v1alpha1.BackfillRequest
	if err := r.Get(t.Context(), req.NamespacedName, &bfr); err != nil {
		if kerrors.IsNotFound(err) {
			return res, nil
		}
		t.Fatalf("Get request: %v", err)
	}
	return res, &bfr
}

func TestReconcileBackfill(t *testing.T) {
	lister := &fakeLister{
		findings: []source.Finding{testSourceFinding(42), testSourceFinding(43)},
		complete: true,
	}
	r, c := newBackfillReconciler(t, testIntegration(), backfillRequest("acme/"), lister, true)

	res, bfr := reconcileBackfill(t, r)
	st := bfr.Status
	if st.Phase != v1alpha1.BackfillComplete || st.Listed != 2 || st.Ingested != 2 || st.Truncated || st.Error != "" {
		t.Errorf("status = %+v, want Complete, listed 2 ingested 2, no truncation, no error", st)
	}
	if st.Attempts != 1 || st.CompletedAt == nil || st.LastAttemptAt == nil {
		t.Errorf("status = %+v, want one attempt and a completion time", st)
	}
	if res.RequeueAfter != DefaultBackfillTTL {
		t.Errorf("requeue = %v, want the TTL", res.RequeueAfter)
	}
	if want := []string{"acme/"}; !slices.Equal(lister.repos, want) {
		t.Errorf("lister filter = %v, want %v", lister.repos, want)
	}
	// The two alerts share one accumulation family, so they fold into a
	// single finding with both alerts.
	items := listFindings(t, c)
	if len(items) != 1 || len(items[0].Spec.Alerts) != 2 {
		t.Fatalf("findings = %d (alerts %v), want one finding holding both alerts", len(items), items)
	}

	// A finished request is not walked again.
	reconcileBackfill(t, r)
	if lister.calls != 1 {
		t.Errorf("lister called %d times after a finished request, want 1", lister.calls)
	}
}

func TestReconcileBackfillExpires(t *testing.T) {
	lister := &fakeLister{complete: true}
	r, _ := newBackfillReconciler(t, testIntegration(), backfillRequest("acme/shop"), lister, true)
	reconcileBackfill(t, r)

	r.Now = func() time.Time { return testClock.Add(DefaultBackfillTTL - time.Hour) }
	if res, bfr := reconcileBackfill(t, r); bfr == nil || res.RequeueAfter != time.Hour {
		t.Fatalf("before the TTL: request %v, requeue %v, want kept for another hour", bfr, res.RequeueAfter)
	}
	r.Now = func() time.Time { return testClock.Add(DefaultBackfillTTL + time.Second) }
	if _, bfr := reconcileBackfill(t, r); bfr != nil {
		t.Errorf("past the TTL the request survives: %+v", bfr.Status)
	}
}

func TestReconcileBackfillTransientFailure(t *testing.T) {
	lister := &fakeLister{err: errors.New("api unreachable")}
	r, _ := newBackfillReconciler(t, testIntegration(), backfillRequest(), lister, true)

	res, bfr := reconcileBackfill(t, r)
	st := bfr.Status
	if st.Phase != v1alpha1.BackfillPending || st.Error == "" || st.Attempts != 1 || st.CompletedAt != nil {
		t.Fatalf("status = %+v, want Pending with the walk error after one attempt", st)
	}
	if res.RequeueAfter != backfillRetryBase {
		t.Errorf("requeue = %v, want the first backoff %v", res.RequeueAfter, backfillRetryBase)
	}

	// The next reconcile retries, and a clean walk completes it.
	lister.err = nil
	lister.complete = true
	_, bfr = reconcileBackfill(t, r)
	if lister.calls != 2 || bfr.Status.Phase != v1alpha1.BackfillComplete || bfr.Status.Error != "" {
		t.Errorf("after retry: calls %d, status %+v, want a clean completion", lister.calls, bfr.Status)
	}
}

func TestReconcileBackfillRetriesRunOut(t *testing.T) {
	lister := &fakeLister{err: errors.New("bad credentials")}
	r, _ := newBackfillReconciler(t, testIntegration(), backfillRequest(), lister, true)
	var bfr *v1alpha1.BackfillRequest
	for range maxBackfillAttempts {
		_, bfr = reconcileBackfill(t, r)
	}
	if bfr.Status.Phase != v1alpha1.BackfillFailed || bfr.Status.Attempts != maxBackfillAttempts ||
		bfr.Status.CompletedAt == nil {
		t.Errorf("status = %+v, want Failed after %d attempts", bfr.Status, maxBackfillAttempts)
	}
}

func TestBackfillBackoff(t *testing.T) {
	for attempts, want := range map[int32]time.Duration{
		1: time.Minute, 2: 2 * time.Minute, 3: 4 * time.Minute, 6: backfillRetryCap, 20: backfillRetryCap,
	} {
		if got := backfillBackoff(attempts); got != want {
			t.Errorf("backfillBackoff(%d) = %v, want %v", attempts, got, want)
		}
	}
}

func TestReconcileBackfillUnsupportedProvider(t *testing.T) {
	r, _ := newBackfillReconciler(t, testIntegration(), backfillRequest(), nil, false)

	_, bfr := reconcileBackfill(t, r)
	if bfr.Status.Phase != v1alpha1.BackfillFailed || !strings.Contains(bfr.Status.Error, "does not support backfill") {
		t.Fatalf("status = %+v, want Failed with the unsupported-provider error", bfr.Status)
	}
}

func TestReconcileBackfillMissingIntegration(t *testing.T) {
	r, _ := newBackfillReconciler(t, nil, backfillRequest(), &fakeLister{}, true)
	_, bfr := reconcileBackfill(t, r)
	if bfr.Status.Phase != v1alpha1.BackfillFailed || !strings.Contains(bfr.Status.Error, "not found") {
		t.Errorf("status = %+v, want Failed: integration not found", bfr.Status)
	}
}

func TestReconcileBackfillTruncated(t *testing.T) {
	lister := &fakeLister{findings: []source.Finding{testSourceFinding(42)}, complete: false}
	r, _ := newBackfillReconciler(t, testIntegration(), backfillRequest(), lister, true)

	_, bfr := reconcileBackfill(t, r)
	if !bfr.Status.Truncated || bfr.Status.Phase != v1alpha1.BackfillComplete {
		t.Fatalf("status = %+v, want Complete and truncated", bfr.Status)
	}
}

// A finding the ingestor rejects is skipped, counted, and surfaced. It
// must not starve the rest of the walk.
func TestReconcileBackfillPartialIngestFailure(t *testing.T) {
	bad := testSourceFinding(41)
	bad.Repo = source.Repo{} // names neither repository nor cloud resource
	lister := &fakeLister{findings: []source.Finding{bad, testSourceFinding(42)}, complete: true}
	r, c := newBackfillReconciler(t, testIntegration(), backfillRequest(), lister, true)

	_, bfr := reconcileBackfill(t, r)
	st := bfr.Status
	if st.Phase != v1alpha1.BackfillComplete || st.Listed != 2 || st.Ingested != 1 || st.Error == "" {
		t.Fatalf("status = %+v, want Complete, listed 2, ingested 1, the ingest error surfaced", st)
	}
	if items := listFindings(t, c); len(items) != 1 {
		t.Errorf("findings = %d, want the good alert ingested", len(items))
	}
}

// A suspended or not-Ready Integration holds the request without spending
// an attempt.
func TestReconcileBackfillHeld(t *testing.T) {
	cases := map[string]func(*v1alpha1.Integration){
		"suspended": func(i *v1alpha1.Integration) { i.Spec.Suspend = true },
		"not ready": func(i *v1alpha1.Integration) {
			i.Status.Conditions = []metav1.Condition{{
				Type: v1alpha1.ConditionReady, Status: metav1.ConditionFalse, Reason: "CredentialInvalid",
				Message: "bad key", LastTransitionTime: metav1.NewTime(testClock),
			}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			integ := testIntegration()
			mutate(integ)
			lister := &fakeLister{complete: true}
			r, _ := newBackfillReconciler(t, integ, backfillRequest(), lister, true)
			res, bfr := reconcileBackfill(t, r)
			if lister.calls != 0 || bfr.Status.Attempts != 0 || bfr.Status.Phase != v1alpha1.BackfillPending ||
				bfr.Status.Error == "" || res.RequeueAfter != backfillWaitRetry {
				t.Errorf("status %+v, calls %d, requeue %v: want held Pending with a reason",
					bfr.Status, lister.calls, res.RequeueAfter)
			}
		})
	}
}

// A requested backfill waits for an operator, runs once approved, and
// never runs once denied.
func TestReconcileBackfillApproval(t *testing.T) {
	requested := func(d *v1alpha1.BackfillDecision) *v1alpha1.BackfillRequest {
		b := backfillRequest("acme/shop")
		b.Spec.Via = v1alpha1.BackfillViaRequested
		b.Spec.Decision = d
		return b
	}
	decision := func(v v1alpha1.BackfillVerdict) *v1alpha1.BackfillDecision {
		return &v1alpha1.BackfillDecision{Verdict: v, By: "ops", At: metav1.NewTime(testClock), Reason: "no"}
	}
	t.Run("undecided waits, then expires", func(t *testing.T) {
		lister := &fakeLister{complete: true}
		r, _ := newBackfillReconciler(t, testIntegration(), requested(nil), lister, true)
		_, bfr := reconcileBackfill(t, r)
		if bfr.Status.Phase != v1alpha1.BackfillAwaitingApproval || lister.calls != 0 {
			t.Fatalf("status %+v, calls %d: want AwaitingApproval and no walk", bfr.Status, lister.calls)
		}
		r.Now = func() time.Time { return testClock.Add(DefaultBackfillTTL + time.Minute) }
		if _, bfr := reconcileBackfill(t, r); bfr != nil {
			t.Errorf("an undecided request outlived the TTL: %+v", bfr.Status)
		}
	})
	t.Run("approved runs", func(t *testing.T) {
		lister := &fakeLister{complete: true}
		r, _ := newBackfillReconciler(t, testIntegration(), requested(decision(v1alpha1.BackfillApproved)), lister, true)
		_, bfr := reconcileBackfill(t, r)
		if bfr.Status.Phase != v1alpha1.BackfillComplete || lister.calls != 1 {
			t.Errorf("status %+v, calls %d: want Complete after one walk", bfr.Status, lister.calls)
		}
	})
	t.Run("denied never runs", func(t *testing.T) {
		lister := &fakeLister{complete: true}
		r, _ := newBackfillReconciler(t, testIntegration(), requested(decision(v1alpha1.BackfillDeniedVerdict)),
			lister, true)
		_, bfr := reconcileBackfill(t, r)
		if bfr.Status.Phase != v1alpha1.BackfillDenied || bfr.Status.Error != "no" || lister.calls != 0 ||
			bfr.Status.CompletedAt == nil {
			t.Errorf("status %+v, calls %d: want Denied with the reason and no walk", bfr.Status, lister.calls)
		}
	})
}

// The default lister construction (no ListerFor): provider and capability
// gating that runs before any credential is read.
func TestListerForGating(t *testing.T) {
	r := &BackfillReconciler{}

	gc := &v1alpha1.Integration{Spec: v1alpha1.IntegrationSpec{
		Provider: v1alpha1.IntegrationProviderGoogleCloud,
	}}
	if _, ok, err := r.listerFor(t.Context(), gc); ok || err != nil {
		t.Errorf("listerFor(google-cloud) = (ok %v, err %v), want unsupported", ok, err)
	}

	gh := testIntegration()
	gh.Spec.GitHub.CodeScanningAlerts = nil
	if _, ok, err := r.listerFor(t.Context(), gh); !ok || err == nil {
		t.Errorf("listerFor(github, no scanning) = (ok %v, err %v), want a retryable error", ok, err)
	}
}

// A GitHub backfill lists every scanning capability the Integration
// enables. The credential read comes after the gate, so a missing Secret
// proves the gate passed.
func TestListerForSecretScanningGate(t *testing.T) {
	r := &BackfillReconciler{Creds: NewCreds(fake.NewClientBuilder().WithScheme(kube.Scheme()).Build())}
	gh := testIntegration()
	gh.Spec.GitHub.CodeScanningAlerts = nil
	gh.Spec.GitHub.SecretScanningAlerts = &v1alpha1.GitHubSecretScanningAlerts{Enabled: true}
	_, ok, err := r.listerFor(t.Context(), gh)
	if !ok || err == nil || strings.Contains(err.Error(), "backfill needs") {
		t.Errorf("listerFor(github, secrets only) = (ok %v, err %v), want past the gate to the credential", ok, err)
	}
}

// fixedLister yields a fixed set of findings.
type fixedLister struct {
	findings []source.Finding
	complete bool
	err      error
}

func (l fixedLister) List(_ context.Context, _ []string, yield func(source.Finding) bool) (bool, error) {
	if l.err != nil {
		return false, l.err
	}
	for _, f := range l.findings {
		if !yield(f) {
			return false, nil
		}
	}
	return l.complete, nil
}

func TestMultiLister(t *testing.T) {
	code := fixedLister{findings: []source.Finding{{Source: "ghas", AlertNumber: 1}}, complete: true}
	secrets := fixedLister{findings: []source.Finding{{Source: "ghas-secrets", AlertNumber: 2}}, complete: true}
	truncated := fixedLister{findings: []source.Finding{{Source: "ghas-secrets", AlertNumber: 3}}}

	tests := []struct {
		name         string
		m            multiLister
		stopAfter    int
		want         []string
		wantComplete bool
		wantErr      bool
	}{
		{"both sources", multiLister{code, secrets}, 0, []string{"ghas#1", "ghas-secrets#2"}, true, false},
		{"one truncated", multiLister{code, truncated}, 0, []string{"ghas#1", "ghas-secrets#3"}, false, false},
		{"yield stops the walk", multiLister{code, secrets}, 1, []string{"ghas#1"}, false, false},
		{"error", multiLister{code, fixedLister{err: errors.New("boom")}}, 0, []string{"ghas#1"}, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			complete, err := tt.m.List(t.Context(), nil, func(f source.Finding) bool {
				got = append(got, fmt.Sprintf("%s#%d", f.Source, f.AlertNumber))
				return tt.stopAfter == 0 || len(got) < tt.stopAfter
			})
			if (err != nil) != tt.wantErr {
				t.Fatalf("List() error = %v, wantErr %v", err, tt.wantErr)
			}
			if complete != tt.wantComplete {
				t.Errorf("complete = %v, want %v", complete, tt.wantComplete)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("listed %v, want %v", got, tt.want)
			}
		})
	}
}

// An Integration change wakes only the unfinished requests naming it.
func TestBackfillRequestsForIntegration(t *testing.T) {
	done := backfillRequest()
	done.Name = "backfill-done"
	done.Status.Phase = v1alpha1.BackfillComplete
	other := backfillRequest()
	other.Name = "backfill-other"
	other.Spec.IntegrationRef.Name = "elsewhere"
	r, c := newBackfillReconciler(t, testIntegration(), backfillRequest(), &fakeLister{}, true)
	for _, b := range []*v1alpha1.BackfillRequest{done, other} {
		if err := c.Create(t.Context(), b); err != nil {
			t.Fatal(err)
		}
	}
	got := r.requestsFor(t.Context(), testIntegration())
	if len(got) != 1 || got[0].Name != "backfill-1" {
		t.Errorf("requestsFor = %v, want only the open request on gh", got)
	}
}
