// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package rollup

import (
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/kube"
	"github.com/bitwise-media-group/patchy/internal/stats"
)

var clock = time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)

func completeInvestigation() *v1alpha1.Investigation {
	return &v1alpha1.Investigation{
		ObjectMeta: metav1.ObjectMeta{
			Name: "finding-aa-1-inv-1", Namespace: "patchy", UID: "inv-uid-1",
			Annotations: map[string]string{v1alpha1.AnnotationRepo: "acme/orders"},
			Finalizers: []string{
				v1alpha1.FinalizerJobs,
				v1alpha1.FinalizerRollupTotal,
				v1alpha1.FinalizerRollupRepository,
				v1alpha1.FinalizerRollupHarness,
				v1alpha1.FinalizerRollupModel,
			},
		},
		Spec: v1alpha1.InvestigationSpec{
			FindingRef: v1alpha1.ObjectReference{Name: "finding-aa-1"}, Attempt: 1,
		},
		Status: v1alpha1.InvestigationStatus{
			Phase: v1alpha1.RunComplete,
			Stage: &v1alpha1.StageResult{
				Outcome: "ok", Harness: "claude", Model: "claude-sonnet-5",
				Usage: v1alpha1.UsageSummary{
					InputTokens: 1000, OutputTokens: 500, CostUSD: "1.250000",
				},
				ElapsedMilliseconds: 60000,
			},
		},
	}
}

func terminalFinding(phase v1alpha1.Phase) *v1alpha1.Finding {
	done := metav1.NewTime(clock.Add(-time.Hour))
	return &v1alpha1.Finding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "finding-aa-1", Namespace: "patchy", UID: "fnd-uid-1",
			Finalizers: []string{v1alpha1.FinalizerRollupTotal, v1alpha1.FinalizerRollupRepository},
		},
		Spec: v1alpha1.FindingSpec{
			IntegrationRef: v1alpha1.LocalObjectReference{Name: "gh"},
			Source:         "ghas",
			Advisories:     []string{"CVE-2026-0001"},
			Repository: &v1alpha1.FindingRepository{
				Type: "github", URL: "https://github.com/acme/orders", Name: "acme/orders",
			},
		},
		Status: v1alpha1.FindingStatus{
			Phase:       phase,
			CompletedAt: &done,
			Attempts:    v1alpha1.AttemptCounts{Investigation: 1, Remediation: 1},
			Investigation: &v1alpha1.InvestigationSummary{
				Name: "finding-aa-1-inv-1", Attempt: 1,
				Recommendation: v1alpha1.RecommendationRemediate,
			},
		},
	}
}

func newReconciler(t *testing.T, objs ...client.Object) (*Reconciler, client.Client) {
	t.Helper()
	c := fake.NewClientBuilder().
		WithScheme(kube.Scheme()).
		WithObjects(objs...).
		WithStatusSubresource(
			&v1alpha1.Finding{}, &v1alpha1.Investigation{},
			&v1alpha1.Remediation{}, &v1alpha1.FindingRollup{},
		).
		Build()
	return &Reconciler{
		Client:    c,
		Namespace: "patchy",
		TTL:       DefaultTTL,
		Now:       func() time.Time { return clock },
	}, c
}

func rollup(t *testing.T, c client.Client, name string) *v1alpha1.FindingRollup {
	t.Helper()
	var fr v1alpha1.FindingRollup
	if err := c.Get(t.Context(), types.NamespacedName{Namespace: "patchy", Name: name}, &fr); err != nil {
		t.Fatalf("Get rollup %s: %v", name, err)
	}
	return &fr
}

func TestChildAggregatesAllScopes(t *testing.T) {
	r, c := newReconciler(t, completeInvestigation())
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "patchy", Name: "finding-aa-1-inv-1"}}
	if _, err := r.ReconcileInvestigation(t.Context(), req); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	total := rollup(t, c, "total")
	agg := total.Status.Bucket.Stages["investigation"]
	if agg.Runs != 1 || agg.Succeeded != 1 || agg.CostMicroUSD != 1_250_000 {
		t.Errorf("total aggregate = %+v", agg)
	}
	if total.Status.Monthly["2026-07"].Runs != 1 {
		t.Errorf("monthly = %+v", total.Status.Monthly)
	}
	repoName := stats.ScopeObjectName(v1alpha1.RollupScope{Type: v1alpha1.ScopeRepository, Key: "acme/orders"})
	if got := rollup(t, c, repoName).Status.Bucket.Stages["investigation"].Runs; got != 1 {
		t.Errorf("repo runs = %d, want 1", got)
	}
	if got := rollup(t, c, "harness-claude").Status.Bucket.Stages["investigation"].Runs; got != 1 {
		t.Errorf("harness runs = %d, want 1", got)
	}
	if got := rollup(t, c, "model-claude-sonnet-5").Status.Bucket.Stages["investigation"].Runs; got != 1 {
		t.Errorf("model runs = %d, want 1", got)
	}

	// Re-reconcile: ledger + conditions make it a no-op.
	if _, err := r.ReconcileInvestigation(t.Context(), req); err != nil {
		t.Fatalf("Reconcile again: %v", err)
	}
	if got := rollup(t, c, "total").Status.Bucket.Stages["investigation"].Runs; got != 1 {
		t.Errorf("total runs = %d after re-reconcile, want 1", got)
	}

	var inv v1alpha1.Investigation
	if err := c.Get(t.Context(), req.NamespacedName, &inv); err != nil {
		t.Fatalf("Get: %v", err)
	}
	for _, cond := range []string{
		v1alpha1.ConditionRolledUpTotal, v1alpha1.ConditionRolledUpRepository,
		v1alpha1.ConditionRolledUpHarness, v1alpha1.ConditionRolledUpModel,
	} {
		if !meta.IsStatusConditionTrue(inv.Status.Conditions, cond) {
			t.Errorf("condition %s not true", cond)
		}
	}
}

func TestChildDeletionReleasesFinalizersAfterAggregation(t *testing.T) {
	inv := completeInvestigation()
	r, c := newReconciler(t, inv)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "patchy", Name: inv.Name}}
	if _, err := r.ReconcileInvestigation(t.Context(), req); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := c.Delete(t.Context(), inv); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := r.ReconcileInvestigation(t.Context(), req); err != nil {
		t.Fatalf("Reconcile deleting: %v", err)
	}
	var cur v1alpha1.Investigation
	err := c.Get(t.Context(), req.NamespacedName, &cur)
	switch {
	case errors.IsNotFound(err):
		// All rollup finalizers released and only FinalizerJobs remained with
		// another owner — acceptable outcome depending on fake GC ordering.
	case err != nil:
		t.Fatalf("Get: %v", err)
	default:
		for _, f := range cur.Finalizers {
			if f != v1alpha1.FinalizerJobs {
				t.Errorf("finalizer %s not released", f)
			}
		}
	}
}

func TestFindingCountsAndTTL(t *testing.T) {
	r, c := newReconciler(t, terminalFinding(v1alpha1.PhaseRemediated))
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "patchy", Name: "finding-aa-1"}}
	res, err := r.ReconcileFinding(t.Context(), req)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	total := rollup(t, c, "total")
	if total.Status.Bucket.Findings != 1 || total.Status.Bucket.Phases["remediated"] != 1 {
		t.Errorf("bucket = %+v", total.Status.Bucket)
	}
	if total.Status.Bucket.Recommendations["remediate"] != 1 {
		t.Errorf("recommendations = %v", total.Status.Bucket.Recommendations)
	}
	if total.Status.Bucket.Attempts != 2 {
		t.Errorf("attempts = %d, want 2", total.Status.Bucket.Attempts)
	}
	// TTL: completed 1h ago, default 336h → requeue for the remainder.
	if want := DefaultTTL - time.Hour; res.RequeueAfter != want {
		t.Errorf("RequeueAfter = %v, want %v", res.RequeueAfter, want)
	}

	// Expired: the finding is deleted, the rollup survives.
	r.Now = func() time.Time { return clock.Add(DefaultTTL) }
	if _, err := r.ReconcileFinding(t.Context(), req); err != nil {
		t.Fatalf("Reconcile expired: %v", err)
	}
	var fnd v1alpha1.Finding
	err = c.Get(t.Context(), req.NamespacedName, &fnd)
	if err == nil && fnd.DeletionTimestamp.IsZero() {
		t.Error("finding not deleted after TTL")
	}
	if got := rollup(t, c, "total").Status.Bucket.Findings; got != 1 {
		t.Errorf("rollup lost data on TTL delete: findings = %d", got)
	}
}

func TestFindingRevivalCorrection(t *testing.T) {
	fnd := terminalFinding(v1alpha1.PhaseHandedOff)
	r, c := newReconciler(t, fnd)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "patchy", Name: "finding-aa-1"}}
	if _, err := r.ReconcileFinding(t.Context(), req); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := rollup(t, c, "total").Status.Bucket.Phases["handedoff"]; got != 1 {
		t.Fatalf("handedoff = %d, want 1", got)
	}

	// Revived and completed again as Remediated.
	var cur v1alpha1.Finding
	if err := c.Get(t.Context(), req.NamespacedName, &cur); err != nil {
		t.Fatalf("Get: %v", err)
	}
	cur.Status.Phase = v1alpha1.PhaseRemediated
	if err := c.Status().Update(t.Context(), &cur); err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, err := r.ReconcileFinding(t.Context(), req); err != nil {
		t.Fatalf("Reconcile revival: %v", err)
	}

	total := rollup(t, c, "total")
	if total.Status.Bucket.Findings != 1 {
		t.Errorf("findings = %d after correction, want 1", total.Status.Bucket.Findings)
	}
	if total.Status.Bucket.Phases["handedoff"] != 0 || total.Status.Bucket.Phases["remediated"] != 1 {
		t.Errorf("phases = %v", total.Status.Bucket.Phases)
	}
}

func TestNonTerminalDeleteCountsAsDeleted(t *testing.T) {
	fnd := terminalFinding(v1alpha1.PhaseRemediating)
	fnd.Status.CompletedAt = nil
	r, c := newReconciler(t, fnd)
	if err := c.Delete(t.Context(), fnd); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "patchy", Name: "finding-aa-1"}}
	if _, err := r.ReconcileFinding(t.Context(), req); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := rollup(t, c, "total").Status.Bucket.Phases["deleted"]; got != 1 {
		t.Errorf("deleted bucket = %d, want 1 (spend was real)", got)
	}
}
