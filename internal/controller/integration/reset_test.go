// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package integration

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/ghclient"
	"github.com/bitwise-media-group/patchy/internal/kube"
)

// fakeResetClient records the demo reset's forge calls.
type fakeResetClient struct {
	deleted    []string
	opened     []string
	failDelete bool
}

func (f *fakeResetClient) DeleteIssue(_ context.Context, repo ghclient.Repo, number int) error {
	if f.failDelete {
		return errors.New("boom")
	}
	f.deleted = append(f.deleted, fmt.Sprintf("%s#%d", repo, number))
	return nil
}

func (f *fakeResetClient) OpenAlert(_ context.Context, repo ghclient.Repo, number int) error {
	f.opened = append(f.opened, fmt.Sprintf("%s#%d", repo, number))
	return nil
}

func newResetReconciler(
	t *testing.T, fc *fakeResetClient, objs ...client.Object,
) (*IntegrationReconciler, client.Client) {
	t.Helper()
	c := fake.NewClientBuilder().
		WithScheme(kube.Scheme()).
		WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.Finding{}, &v1alpha1.Integration{}).
		Build()
	r := &IntegrationReconciler{
		Client: c,
		ClientFor: func(context.Context, *v1alpha1.Integration, ghclient.Repo) (resetClient, error) {
			return fc, nil
		},
	}
	return r, c
}

func TestRunReset(t *testing.T) {
	tracked := projectable(v1alpha1.PhaseDismissed)
	tracked.Status.Tracking = &v1alpha1.TrackingStatus{Integration: "gh", IssueNumber: 7}
	untracked := projectable(v1alpha1.PhaseOpened)
	untracked.Name = "finding-bb-1"

	fc := &fakeResetClient{}
	r, c := newResetReconciler(t, fc,
		testIntegration(), tracked, untracked,
		&v1alpha1.Investigation{ObjectMeta: metav1.ObjectMeta{Name: "inv-1", Namespace: "patchy"}},
		&v1alpha1.Remediation{ObjectMeta: metav1.ObjectMeta{Name: "rem-1", Namespace: "patchy"}},
		&v1alpha1.Repository{ObjectMeta: metav1.ObjectMeta{Name: "repo-1", Namespace: "patchy"}},
		&v1alpha1.FindingRollup{ObjectMeta: metav1.ObjectMeta{Name: "total", Namespace: "patchy"}},
	)

	if err := r.runReset(t.Context(), "patchy"); err != nil {
		t.Fatalf("runReset() error = %v", err)
	}

	if want := []string{"acme/orders#7"}; !slices.Equal(fc.deleted, want) {
		t.Errorf("deleted issues = %v, want %v", fc.deleted, want)
	}
	// Only the dismissed finding's own alert (#42) is restored — never a
	// repository-wide sweep, and never alerts of non-dismissed findings.
	if want := []string{"acme/orders#42"}; !slices.Equal(fc.opened, want) {
		t.Errorf("reopened alerts = %v, want %v", fc.opened, want)
	}

	for _, list := range []client.ObjectList{
		&v1alpha1.FindingList{}, &v1alpha1.InvestigationList{}, &v1alpha1.RemediationList{},
		&v1alpha1.RepositoryList{}, &v1alpha1.FindingRollupList{},
	} {
		if err := c.List(t.Context(), list, client.InNamespace("patchy")); err != nil {
			t.Fatalf("list %T: %v", list, err)
		}
		if n := len(listItems(t, list)); n != 0 {
			t.Errorf("%T holds %d items after reset, want 0", list, n)
		}
	}
	var integs v1alpha1.IntegrationList
	if err := c.List(t.Context(), &integs, client.InNamespace("patchy")); err != nil {
		t.Fatalf("list integrations: %v", err)
	}
	if len(integs.Items) != 1 {
		t.Errorf("integrations = %d, want the configuration untouched", len(integs.Items))
	}
}

func TestRunResetKeepsStateOnForgeFailure(t *testing.T) {
	tracked := projectable(v1alpha1.PhaseDismissed)
	tracked.Status.Tracking = &v1alpha1.TrackingStatus{Integration: "gh", IssueNumber: 7}
	fc := &fakeResetClient{failDelete: true}
	r, c := newResetReconciler(t, fc, testIntegration(), tracked)

	if err := r.runReset(t.Context(), "patchy"); err == nil {
		t.Fatal("runReset() = nil, want the forge failure surfaced")
	}

	// The Findings carry the issue numbers a retry needs; they must survive.
	var findings v1alpha1.FindingList
	if err := c.List(t.Context(), &findings, client.InNamespace("patchy")); err != nil {
		t.Fatalf("list findings: %v", err)
	}
	if len(findings.Items) != 1 {
		t.Errorf("findings = %d after failed reset, want 1 (kept for retry)", len(findings.Items))
	}
}

func TestConsumeResetDropsDedupAndEchoes(t *testing.T) {
	fc := &fakeResetClient{}
	r, _ := newResetReconciler(t, fc, testIntegration())
	dropped := false
	r.ResetDedup = func() { dropped = true }

	integ := testIntegration()
	at := metav1.Now()
	if err := r.consumeReset(t.Context(), integ, &v1alpha1.ActionRequest{By: "op", At: at}); err != nil {
		t.Fatalf("consumeReset() error = %v", err)
	}
	if !dropped {
		t.Error("dedup window not dropped")
	}
	if integ.Status.ResetAt == nil || !integ.Status.ResetAt.Equal(&at) {
		t.Errorf("status.resetAt = %v, want the request echoed", integ.Status.ResetAt)
	}
}

// listItems extracts the items of any ObjectList via the meta accessor.
func listItems(t *testing.T, list client.ObjectList) []client.Object {
	t.Helper()
	items, err := meta.ExtractList(list)
	if err != nil {
		t.Fatalf("extract list: %v", err)
	}
	out := make([]client.Object, 0, len(items))
	for _, it := range items {
		out = append(out, it.(client.Object))
	}
	return out
}
