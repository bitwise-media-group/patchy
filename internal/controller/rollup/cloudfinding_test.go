// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package rollup

import (
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
)

// repoLessFinding is a cloud finding whose resource carried no ownership
// labels, so it reached a terminal phase without ever having a repository.
func repoLessFinding(phase v1alpha1.Phase) *v1alpha1.Finding {
	fnd := terminalFinding(phase)
	fnd.Spec.Source = "gcp-scc"
	fnd.Spec.Repository = nil
	fnd.Spec.CloudResource = &v1alpha1.FindingCloudResource{
		Provider: v1alpha1.CloudProviderGoogle,
		Name:     "//storage.googleapis.com/projects/acme-prod/buckets/artifacts",
	}
	return fnd
}

// A repository-scoped rollup has no key to file a repo-less finding under.
// Until cloud findings existed this branch was unreachable, because every
// GHAS finding has a repository; now it is routine, so the accounting has to
// be right rather than merely untested.
//
// "Right" means: the total scope counts it, the repository scope does not,
// and both scopes still settle — an unsettled scope holds its finalizer and
// the finding never goes away.
func TestRepoLessFindingCountsOnlyAtTotalScope(t *testing.T) {
	r, c := newReconciler(t, repoLessFinding(v1alpha1.PhaseHandedOff))
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "patchy", Name: "finding-aa-1"}}
	if _, err := r.ReconcileFinding(t.Context(), req); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got := rollup(t, c, "total").Status.Bucket.Findings; got != 1 {
		t.Errorf("total findings = %d, want 1: a repo-less finding still happened", got)
	}

	var fnd v1alpha1.Finding
	if err := c.Get(t.Context(), req.NamespacedName, &fnd); err != nil {
		t.Fatalf("Get: %v", err)
	}
	for _, cond := range []string{
		v1alpha1.ConditionRolledUpTotal,
		v1alpha1.ConditionRolledUpRepository,
	} {
		if !meta.IsStatusConditionTrue(fnd.Status.Conditions, cond) {
			t.Errorf("%s is not True; an unsettled scope keeps its finalizer and the finding never expires", cond)
		}
	}
	// The repository scope settles with no ledger entry, which is what makes
	// the reversal below a no-op rather than a double-count in the other
	// direction.
	repoCond := meta.FindStatusCondition(fnd.Status.Conditions, v1alpha1.ConditionRolledUpRepository)
	if repoCond.Reason != "NoScopeKey" || repoCond.Message != "" {
		t.Errorf("repository condition = %+v, want NoScopeKey with no counted phase", repoCond)
	}
}

// Reviving a repo-less finding must not reverse a repository-scope count that
// was never made. The count and its reversal derive the scope key
// independently, so an imbalance here would drive a rollup negative with
// nothing to show why.
func TestRepoLessFindingRevivalIsBalanced(t *testing.T) {
	r, c := newReconciler(t, repoLessFinding(v1alpha1.PhaseHandedOff))
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "patchy", Name: "finding-aa-1"}}
	if _, err := r.ReconcileFinding(t.Context(), req); err != nil {
		t.Fatalf("Reconcile terminal: %v", err)
	}

	// An approval revives it out of the terminal phase.
	var fnd v1alpha1.Finding
	if err := c.Get(t.Context(), req.NamespacedName, &fnd); err != nil {
		t.Fatalf("Get: %v", err)
	}
	fnd.Status.Phase = v1alpha1.PhaseQueued
	fnd.Status.CompletedAt = nil
	if err := c.Status().Update(t.Context(), &fnd); err != nil {
		t.Fatalf("revive: %v", err)
	}
	if _, err := r.ReconcileFinding(t.Context(), req); err != nil {
		t.Fatalf("Reconcile revived: %v", err)
	}

	// No repository rollup should exist at all: a repo-less finding was never
	// counted against one, so there was nothing to reverse and nothing to
	// create. A rollup here at any value means the two sides disagreed.
	var repos v1alpha1.FindingRollupList
	if err := c.List(t.Context(), &repos); err != nil {
		t.Fatalf("List rollups: %v", err)
	}
	for i := range repos.Items {
		if repos.Items[i].Spec.Scope.Type == v1alpha1.ScopeRepository {
			t.Errorf("a repository rollup exists for a repo-less finding: %s = %+v",
				repos.Items[i].Name, repos.Items[i].Status.Bucket)
		}
	}
	if got := rollup(t, c, "total").Status.Bucket.Findings; got != 0 {
		t.Errorf("total findings = %d, want 0 after the revival reversed the count", got)
	}
}

// A cloud finding that did resolve a repository is accounted for exactly like
// a code finding — the resolution happened before any of this, so nothing
// here needs to know it was ever repo-less.
func TestResolvedCloudFindingCountsAtBothScopes(t *testing.T) {
	fnd := repoLessFinding(v1alpha1.PhaseRemediated)
	fnd.Spec.Repository = &v1alpha1.FindingRepository{
		Type: "github", URL: "https://github.com/acme/infra", Name: "acme/infra",
	}
	r, c := newReconciler(t, fnd)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "patchy", Name: "finding-aa-1"}}
	if _, err := r.ReconcileFinding(t.Context(), req); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	var got v1alpha1.Finding
	if err := c.Get(t.Context(), req.NamespacedName, &got); err != nil {
		t.Fatalf("Get: %v", err)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, v1alpha1.ConditionRolledUpRepository)
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason == "NoScopeKey" {
		t.Errorf("repository condition = %+v, want a real count once a repository resolved", cond)
	}
}
