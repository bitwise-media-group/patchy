// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package integration

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/kube"
	"github.com/bitwise-media-group/patchy/pkg/source"
)

func TestPendingBackfill(t *testing.T) {
	at := metav1.NewTime(time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC))
	earlier := metav1.NewTime(at.Add(-time.Hour))
	tests := []struct {
		name    string
		integ   v1alpha1.Integration
		pending bool
	}{
		{"no request", v1alpha1.Integration{}, false},
		{
			"unhandled request",
			v1alpha1.Integration{Spec: v1alpha1.IntegrationSpec{Backfill: &v1alpha1.BackfillRequest{At: at}}},
			true,
		},
		{
			"handled request",
			v1alpha1.Integration{
				Spec:   v1alpha1.IntegrationSpec{Backfill: &v1alpha1.BackfillRequest{At: at}},
				Status: v1alpha1.IntegrationStatus{Backfill: &v1alpha1.BackfillStatus{BackfilledAt: &at}},
			},
			false,
		},
		{
			"newer request supersedes the handled one",
			v1alpha1.Integration{
				Spec:   v1alpha1.IntegrationSpec{Backfill: &v1alpha1.BackfillRequest{At: at}},
				Status: v1alpha1.IntegrationStatus{Backfill: &v1alpha1.BackfillStatus{BackfilledAt: &earlier}},
			},
			true,
		},
		{
			"failed run left no echo",
			v1alpha1.Integration{
				Spec: v1alpha1.IntegrationSpec{Backfill: &v1alpha1.BackfillRequest{At: at}},
				Status: v1alpha1.IntegrationStatus{
					Backfill: &v1alpha1.BackfillStatus{LastRunAt: &at, Error: "boom"},
				},
			},
			true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pendingBackfill(&tt.integ) != nil; got != tt.pending {
				t.Errorf("pendingBackfill() != nil = %v, want %v", got, tt.pending)
			}
		})
	}
}

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

// newBackfillReconciler builds a Reconcile-level harness: a PAT-secret
// Integration (Creds.Validate passes offline), a real Ingestor over the
// fake client, and the ListerFor seam.
func newBackfillReconciler(
	t *testing.T, integ *v1alpha1.Integration, lister *fakeLister, listerOK bool,
) (*IntegrationReconciler, client.Client) {
	t.Helper()
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "creds", Namespace: "patchy"},
		Data: map[string][]byte{
			"token":         []byte("pat-token"),
			"webhookSecret": []byte("hmac"),
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(kube.Scheme()).
		WithObjects(integ, secret).
		WithStatusSubresource(&v1alpha1.Finding{}, &v1alpha1.Integration{}).
		WithIndex(&v1alpha1.Finding{}, KeyHashIndex, KeyHashIndexer).
		Build()
	r := &IntegrationReconciler{
		Client: c,
		Creds:  NewCreds(c),
		Now:    func() time.Time { return testClock },
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

func backfillIntegration(at metav1.Time, repos ...string) *v1alpha1.Integration {
	integ := testIntegration()
	integ.Spec.Backfill = &v1alpha1.BackfillRequest{By: "dev", At: at, Repositories: repos}
	return integ
}

func reconcileIntegration(t *testing.T, r *IntegrationReconciler) v1alpha1.Integration {
	t.Helper()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "patchy", Name: "gh"}}
	if _, err := r.Reconcile(t.Context(), req); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	var integ v1alpha1.Integration
	if err := r.Get(t.Context(), req.NamespacedName, &integ); err != nil {
		t.Fatalf("Get integration: %v", err)
	}
	return integ
}

func TestReconcileBackfill(t *testing.T) {
	at := metav1.NewTime(testClock)
	lister := &fakeLister{
		findings: []source.Finding{testSourceFinding(42), testSourceFinding(43)},
		complete: true,
	}
	r, c := newBackfillReconciler(t, backfillIntegration(at, "acme/"), lister, true)

	integ := reconcileIntegration(t, r)
	st := integ.Status.Backfill
	if st == nil {
		t.Fatal("status.backfill = nil, want a run report")
	}
	if st.Listed != 2 || st.Ingested != 2 || st.Truncated || st.Error != "" {
		t.Errorf("status.backfill = %+v, want listed 2 ingested 2, no truncation, no error", st)
	}
	if st.BackfilledAt == nil || !st.BackfilledAt.Time.Equal(at.Time) {
		t.Errorf("backfilledAt = %v, want the request echo %v", st.BackfilledAt, at)
	}
	if st.AttemptedAt == nil || !st.AttemptedAt.Time.Equal(at.Time) {
		t.Errorf("attemptedAt = %v, want the request echo %v", st.AttemptedAt, at)
	}
	if want := []string{"acme/"}; len(lister.repos) != 1 || lister.repos[0] != want[0] {
		t.Errorf("lister filter = %v, want %v", lister.repos, want)
	}
	// The two alerts share one accumulation family, so they fold into a
	// single finding with both alerts.
	items := listFindings(t, c)
	if len(items) != 1 || len(items[0].Spec.Alerts) != 2 {
		t.Fatalf("findings = %d (alerts %v), want one finding holding both alerts",
			len(items), items)
	}

	// A handled request is not re-run.
	reconcileIntegration(t, r)
	if lister.calls != 1 {
		t.Errorf("lister called %d times after a handled request, want 1", lister.calls)
	}
}

func TestReconcileBackfillTransientFailure(t *testing.T) {
	at := metav1.NewTime(testClock)
	lister := &fakeLister{err: errors.New("api unreachable")}
	r, _ := newBackfillReconciler(t, backfillIntegration(at), lister, true)

	integ := reconcileIntegration(t, r)
	st := integ.Status.Backfill
	if st == nil || st.Error == "" {
		t.Fatalf("status.backfill = %+v, want the walk error", st)
	}
	if st.BackfilledAt != nil {
		t.Errorf("backfilledAt = %v after a failed walk, want nil (retry stays pending)", st.BackfilledAt)
	}
	// The attempt is echoed even on failure, so observers (the status page)
	// can tell a failed attempt from a request the controller never saw.
	if st.AttemptedAt == nil || !st.AttemptedAt.Time.Equal(at.Time) {
		t.Errorf("attemptedAt = %v after a failed walk, want the request echo %v", st.AttemptedAt, at)
	}

	// The failed request stays pending: the next reconcile retries.
	lister.err = nil
	lister.complete = true
	integ = reconcileIntegration(t, r)
	if lister.calls != 2 {
		t.Errorf("lister called %d times, want a retry after the failure", lister.calls)
	}
	if st = integ.Status.Backfill; st == nil || st.BackfilledAt == nil || st.Error != "" {
		t.Errorf("status.backfill after retry = %+v, want a clean echo", st)
	}
}

func TestReconcileBackfillUnsupportedProvider(t *testing.T) {
	at := metav1.NewTime(testClock)
	r, _ := newBackfillReconciler(t, backfillIntegration(at), nil, false)

	integ := reconcileIntegration(t, r)
	st := integ.Status.Backfill
	if st == nil || !strings.Contains(st.Error, "does not support backfill") {
		t.Fatalf("status.backfill = %+v, want the unsupported-provider error", st)
	}
	// Consumed, not retried: the echo lands despite the error.
	if st.BackfilledAt == nil || !st.BackfilledAt.Time.Equal(at.Time) {
		t.Errorf("backfilledAt = %v, want the request echo (consumed once)", st.BackfilledAt)
	}
}

func TestReconcileBackfillTruncated(t *testing.T) {
	at := metav1.NewTime(testClock)
	lister := &fakeLister{findings: []source.Finding{testSourceFinding(42)}, complete: false}
	r, _ := newBackfillReconciler(t, backfillIntegration(at), lister, true)

	integ := reconcileIntegration(t, r)
	st := integ.Status.Backfill
	if st == nil || !st.Truncated {
		t.Fatalf("status.backfill = %+v, want truncated", st)
	}
	if st.BackfilledAt == nil {
		t.Error("backfilledAt = nil, want the echo: a truncated walk still consumed the request")
	}
}

// A finding the ingestor rejects is skipped, counted, and surfaced — it
// must not starve the rest of the walk.
func TestReconcileBackfillPartialIngestFailure(t *testing.T) {
	at := metav1.NewTime(testClock)
	bad := testSourceFinding(41)
	bad.Repo = source.Repo{} // names neither repository nor cloud resource
	lister := &fakeLister{findings: []source.Finding{bad, testSourceFinding(42)}, complete: true}
	r, c := newBackfillReconciler(t, backfillIntegration(at), lister, true)

	integ := reconcileIntegration(t, r)
	st := integ.Status.Backfill
	if st == nil || st.Listed != 2 || st.Ingested != 1 || st.Error == "" {
		t.Fatalf("status.backfill = %+v, want listed 2, ingested 1, the ingest error surfaced", st)
	}
	if st.BackfilledAt == nil {
		t.Error("backfilledAt = nil, want the echo despite one bad alert")
	}
	if items := listFindings(t, c); len(items) != 1 {
		t.Errorf("findings = %d, want the good alert ingested", len(items))
	}
}

func TestReconcileBackfillSuspended(t *testing.T) {
	at := metav1.NewTime(testClock)
	integ := backfillIntegration(at)
	integ.Spec.Suspend = true
	lister := &fakeLister{complete: true}
	r, _ := newBackfillReconciler(t, integ, lister, true)

	got := reconcileIntegration(t, r)
	if got.Status.Backfill != nil || lister.calls != 0 {
		t.Errorf("suspended integration ran a backfill (status %+v, calls %d), want untouched",
			got.Status.Backfill, lister.calls)
	}
}

// The default (seam-less) lister construction: provider and capability
// gating that runs before any credential is read.
func TestListerForGating(t *testing.T) {
	r := &IntegrationReconciler{}

	gc := &v1alpha1.Integration{Spec: v1alpha1.IntegrationSpec{
		Provider: v1alpha1.IntegrationProviderGoogleCloud,
	}}
	if _, ok, err := r.listerFor(t.Context(), gc); ok || err != nil {
		t.Errorf("listerFor(google-cloud) = (ok %v, err %v), want unsupported", ok, err)
	}

	gh := testIntegration()
	gh.Spec.GitHub.CodeScanningAlerts = nil
	if _, ok, err := r.listerFor(t.Context(), gh); !ok || err == nil {
		t.Errorf("listerFor(github, no code scanning) = (ok %v, err %v), want a retryable error", ok, err)
	}
}
