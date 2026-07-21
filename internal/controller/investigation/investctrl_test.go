// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package investigation

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/envelope"
	"github.com/bitwise-media-group/patchy/internal/forge"
	"github.com/bitwise-media-group/patchy/internal/jobs"
	"github.com/bitwise-media-group/patchy/internal/kube"
)

var clock = time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)

// enhancedFinding is past its window and age, ready for the gate.
func enhancedFinding() *v1alpha1.Finding {
	early := metav1.NewTime(clock.Add(-3 * time.Hour))
	return &v1alpha1.Finding{
		ObjectMeta: metav1.ObjectMeta{Name: "finding-aa-1", Namespace: "patchy", UID: "uid-1"},
		Spec: v1alpha1.FindingSpec{
			IntegrationRef: v1alpha1.LocalObjectReference{Name: "gh"},
			Source:         "ghas",
			Advisories:     []string{"CVE-2026-0001"},
			Severity:       v1alpha1.LevelHigh,
			Repository: &v1alpha1.FindingRepository{
				Type: "github", URL: "https://github.com/acme/orders", Name: "acme/orders",
			},
		},
		Status: v1alpha1.FindingStatus{
			Phase:           v1alpha1.PhaseEnhanced,
			FirstObservedAt: &early,
			Conditions: []metav1.Condition{{
				Type: v1alpha1.ConditionAccumulationComplete, Status: metav1.ConditionTrue,
				Reason: "WindowElapsed", LastTransitionTime: early,
			}},
		},
	}
}

func testForge() *v1alpha1.Forge {
	return &v1alpha1.Forge{
		ObjectMeta: metav1.ObjectMeta{Name: "gh-forge", Namespace: "patchy"},
		Spec: v1alpha1.ForgeSpec{
			Provider:  v1alpha1.ForgeProviderGitHub,
			SecretRef: v1alpha1.LocalSecretReference{Name: "creds"},
		},
	}
}

func newGate(t *testing.T, objs ...client.Object) (*GateReconciler, client.Client) {
	t.Helper()
	c := fake.NewClientBuilder().
		WithScheme(kube.Scheme()).
		WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.Finding{}, &v1alpha1.Repository{}, &v1alpha1.Investigation{}).
		Build()
	return &GateReconciler{
		Client:    c,
		Forges:    forge.NewStore(c),
		Namespace: "patchy",
		MinAge:    time.Hour,
		Now:       func() time.Time { return clock },
	}, c
}

func gateOnce(t *testing.T, r *GateReconciler) ctrl.Result {
	t.Helper()
	res, err := r.Reconcile(t.Context(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "patchy", Name: "finding-aa-1"},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return res
}

// fndName is the single finding every case uses.
const fndName = "finding-aa-1"

func getF(t *testing.T, c client.Client) *v1alpha1.Finding {
	t.Helper()
	var f v1alpha1.Finding
	if err := c.Get(t.Context(), types.NamespacedName{Namespace: "patchy", Name: fndName}, &f); err != nil {
		t.Fatalf("Get(%s): %v", fndName, err)
	}
	return &f
}

func TestGateCreatesRepositoryThenInvestigation(t *testing.T) {
	r, c := newGate(t, enhancedFinding(), testForge())

	// Pass 1: Repository created, finding still Enhanced.
	gateOnce(t, r)
	var repo v1alpha1.Repository
	if err := c.Get(t.Context(), types.NamespacedName{Namespace: "patchy", Name: "finding-aa-1-src"}, &repo); err != nil {
		t.Fatalf("Repository not created: %v", err)
	}
	if got := getF(t, c).Status.Phase; got != v1alpha1.PhaseEnhanced {
		t.Fatalf("phase = %q before artifact ready, want Enhanced", got)
	}

	// Artifact becomes ready → pass 2 opens the investigation.
	meta.SetStatusCondition(&repo.Status.Conditions, metav1.Condition{
		Type: v1alpha1.ConditionReady, Status: metav1.ConditionTrue, Reason: "ArtifactReady",
	})
	repo.Status.ResolvedSHA = "abc123"
	repo.Status.Artifact = &v1alpha1.Artifact{URL: "http://arts/x.tar.gz", Digest: "d"}
	if err := c.Status().Update(t.Context(), &repo); err != nil {
		t.Fatalf("update repo status: %v", err)
	}
	gateOnce(t, r)

	f := getF(t, c)
	if f.Status.Phase != v1alpha1.PhaseInvestigating {
		t.Errorf("phase = %q, want Investigating", f.Status.Phase)
	}
	if f.Status.Attempts.Investigation != 1 || f.Status.ActiveRun == nil {
		t.Errorf("attempts/activeRun = %+v/%+v", f.Status.Attempts, f.Status.ActiveRun)
	}
	var inv v1alpha1.Investigation
	invKey := types.NamespacedName{Namespace: "patchy", Name: "finding-aa-1-inv-1"}
	if err := c.Get(t.Context(), invKey, &inv); err != nil {
		t.Fatalf("Investigation not created: %v", err)
	}
	if inv.Spec.FindingRef.UID != "uid-1" || inv.Spec.Attempt != 1 {
		t.Errorf("investigation spec = %+v", inv.Spec)
	}
	if len(inv.Finalizers) != 5 {
		t.Errorf("finalizers = %v, want jobs + 4 rollup scopes", inv.Finalizers)
	}
}

func TestGateWaitsForMinAge(t *testing.T) {
	fnd := enhancedFinding()
	recent := metav1.NewTime(clock.Add(-10 * time.Minute))
	fnd.Status.FirstObservedAt = &recent
	r, _ := newGate(t, fnd, testForge())
	res := gateOnce(t, r)
	if res.RequeueAfter != 50*time.Minute {
		t.Errorf("RequeueAfter = %v, want 50m (age gate)", res.RequeueAfter)
	}
}

func TestGateWaitsForAccumulation(t *testing.T) {
	fnd := enhancedFinding()
	fnd.Status.Conditions = nil
	r, c := newGate(t, fnd, testForge())
	gateOnce(t, r)
	if got := getF(t, c).Status.Phase; got != v1alpha1.PhaseEnhanced {
		t.Errorf("phase = %q, want Enhanced (window open)", got)
	}
}

func TestGateParksRepoLessFinding(t *testing.T) {
	fnd := enhancedFinding()
	fnd.Spec.Repository = nil
	r, c := newGate(t, fnd, testForge())
	gateOnce(t, r)
	f := getF(t, c)
	if f.Status.Phase != v1alpha1.PhaseHandedOff {
		t.Errorf("phase = %q, want HandedOff for repo-less finding", f.Status.Phase)
	}
	cond := meta.FindStatusCondition(f.Status.Conditions, v1alpha1.ConditionForgeResolved)
	if cond == nil || cond.Reason != v1alpha1.ReasonNoRepository {
		t.Errorf("ForgeResolved = %+v, want NoRepository", cond)
	}
}

func TestGateNoForgeStaysEnhanced(t *testing.T) {
	r, c := newGate(t, enhancedFinding()) // no forges configured
	gateOnce(t, r)
	f := getF(t, c)
	if f.Status.Phase != v1alpha1.PhaseEnhanced {
		t.Errorf("phase = %q, want Enhanced (recoverable park)", f.Status.Phase)
	}
	cond := meta.FindStatusCondition(f.Status.Conditions, v1alpha1.ConditionForgeResolved)
	if cond == nil || cond.Reason != v1alpha1.ReasonNoForgeMatch {
		t.Errorf("ForgeResolved = %+v, want NoForgeMatch", cond)
	}
}

// fakeRunner is the jobs seam.
type fakeRunner struct {
	created []jobs.Spec
	done    bool
	events  []envelope.Event
}

func (f *fakeRunner) Create(_ context.Context, spec jobs.Spec) (string, error) {
	f.created = append(f.created, spec)
	return "job-1", nil
}

func (f *fakeRunner) Result(context.Context, string) ([]envelope.Event, error) {
	return f.events, nil
}

func (f *fakeRunner) Status(context.Context, string) (jobs.Status, error) {
	st := jobs.Status{Done: f.done}
	if f.done {
		st.Succeeded = 1
	}
	return st, nil
}

func (f *fakeRunner) Delete(context.Context, string) error { return nil }

// investigationFixture is a granted Investigation with its finding + repo.
func investigationFixture() []client.Object {
	fnd := enhancedFinding()
	fnd.Status.Phase = v1alpha1.PhaseInvestigating
	fnd.Status.Attempts.Investigation = 1
	inv := &v1alpha1.Investigation{
		ObjectMeta: metav1.ObjectMeta{
			Name: "finding-aa-1-inv-1", Namespace: "patchy",
			Labels: map[string]string{
				v1alpha1.LabelFinding: "finding-aa-1", v1alpha1.LabelSeverity: "high",
			},
		},
		Spec: v1alpha1.InvestigationSpec{
			FindingRef:    v1alpha1.ObjectReference{Name: "finding-aa-1", UID: "uid-1"},
			Attempt:       1,
			RepositoryRef: &v1alpha1.LocalObjectReference{Name: "finding-aa-1-src"},
		},
		Status: v1alpha1.InvestigationStatus{
			Phase:  v1alpha1.RunRunning,
			JobRef: &v1alpha1.JobReference{Name: "job-1"},
		},
	}
	return []client.Object{fnd, inv}
}

func newInvestigation(
	t *testing.T, runner *fakeRunner, objs ...client.Object,
) (*InvestigationReconciler, client.Client) {
	t.Helper()
	c := fake.NewClientBuilder().
		WithScheme(kube.Scheme()).
		WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.Finding{}, &v1alpha1.Investigation{}).
		Build()
	return &InvestigationReconciler{
		Client:              c,
		Runner:              runner,
		Namespace:           "patchy",
		MaxConcurrent:       2,
		MaxAttempts:         2,
		ConfidenceThreshold: 0.75,
		Now:                 func() time.Time { return clock },
	}, c
}

func investigationEvent(rec string, confidence float64, await bool) []envelope.Event {
	return []envelope.Event{{
		V: envelope.Version, Type: envelope.TypeInvestigation, Finding: "finding-aa-1",
		Investigation: &envelope.Investigation{
			Stage: envelope.Stage{Outcome: envelope.OutcomeOK, Harness: "claude", Model: "claude-sonnet-5",
				Usage: envelope.Usage{OutputTokens: 500, CostUSD: 1.25}, ElapsedSeconds: 60},
			ReportMarkdown: "---\nfrontmatter\n---\nanalysis",
			Exploitability: envelope.AnalysisResult{Rating: "high", Summary: "reachable"},
			Likelihood:     envelope.AnalysisResult{Rating: "medium", Summary: "needs auth"},
			Impact:         envelope.AnalysisResult{Rating: "critical", Summary: "rce"},
			Recommendation: rec, Priority: "high", Severity: "high",
			Confidence: confidence, AwaitApproval: await,
			RemediationModel: "claude-sonnet-5", MaxTurns: 40, TokenBudget: 200000,
		},
	}}
}

func applyOnce(t *testing.T, r *InvestigationReconciler) {
	t.Helper()
	if _, err := r.Reconcile(t.Context(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "patchy", Name: "finding-aa-1-inv-1"},
	}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
}

func TestApplyRoutesVerdicts(t *testing.T) {
	cases := []struct {
		name       string
		rec        string
		confidence float64
		await      bool
		wantPhase  v1alpha1.Phase
	}{
		{"confident remediate queues", "remediate", 0.9, false, v1alpha1.PhaseQueued},
		{"low confidence holds", "remediate", 0.5, false, v1alpha1.PhaseAwaitingApproval},
		{"breaking-change holds", "remediate", 0.95, true, v1alpha1.PhaseAwaitingApproval},
		{"ignore dismisses", "ignore", 0.9, false, v1alpha1.PhaseDismissed},
		{"manual hands off", "manual", 0.9, false, v1alpha1.PhaseHandedOff},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := &fakeRunner{done: true, events: investigationEvent(tc.rec, tc.confidence, tc.await)}
			r, c := newInvestigation(t, runner, investigationFixture()...)
			applyOnce(t, r)

			f := getF(t, c)
			if f.Status.Phase != tc.wantPhase {
				t.Errorf("phase = %q, want %q", f.Status.Phase, tc.wantPhase)
			}
			if f.Status.Investigation == nil || f.Status.Investigation.Exploitability != "high" {
				t.Errorf("summary = %+v, want exploitability high", f.Status.Investigation)
			}
			var inv v1alpha1.Investigation
			key := types.NamespacedName{Namespace: "patchy", Name: "finding-aa-1-inv-1"}
			if err := c.Get(t.Context(), key, &inv); err != nil {
				t.Fatalf("Get investigation: %v", err)
			}
			if inv.Status.Phase != v1alpha1.RunComplete {
				t.Errorf("investigation phase = %q, want Complete", inv.Status.Phase)
			}
			if inv.Status.Stage == nil || inv.Status.Stage.Usage.CostUSD != "1.250000" {
				t.Errorf("stage = %+v, want cost 1.250000", inv.Status.Stage)
			}
			if inv.Status.Report == "" {
				t.Error("report not stamped on the child")
			}
		})
	}
}

func TestApplyFailureRetriesThenExhausts(t *testing.T) {
	// Attempt 1 of 2: revert to Enhanced.
	runner := &fakeRunner{done: true, events: []envelope.Event{{
		V: envelope.Version, Type: envelope.TypeInvestigation,
		Investigation: &envelope.Investigation{Stage: envelope.Stage{
			Outcome: envelope.OutcomeTimeout, Detail: "wall clock",
		}},
	}}}
	r, c := newInvestigation(t, runner, investigationFixture()...)
	applyOnce(t, r)
	if got := getF(t, c).Status.Phase; got != v1alpha1.PhaseEnhanced {
		t.Fatalf("phase = %q after attempt 1 failure, want Enhanced", got)
	}

	// Attempt 2 of 2: exhausted.
	objs := investigationFixture()
	inv := objs[1].(*v1alpha1.Investigation)
	inv.Name = "finding-aa-1-inv-2"
	inv.Spec = v1alpha1.InvestigationSpec{
		FindingRef: v1alpha1.ObjectReference{Name: "finding-aa-1", UID: "uid-1"}, Attempt: 2,
		RepositoryRef: &v1alpha1.LocalObjectReference{Name: "finding-aa-1-src"},
	}
	r2, c2 := newInvestigation(t, runner, objs...)
	if _, err := r2.Reconcile(t.Context(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "patchy", Name: "finding-aa-1-inv-2"},
	}); err != nil {
		t.Fatalf("Reconcile attempt 2: %v", err)
	}
	if got := getFNamed(t, c2).Status.Phase; got != v1alpha1.PhaseFailed {
		t.Errorf("phase = %q after exhausted attempts, want Failed", got)
	}
}

func getFNamed(t *testing.T, c client.Client) *v1alpha1.Finding {
	t.Helper()
	return getF(t, c)
}

func TestSchedulerBoundsAndOrdersGrants(t *testing.T) {
	mk := func(name, severity string, created time.Time) *v1alpha1.Investigation {
		return &v1alpha1.Investigation{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: "patchy",
				Labels:            map[string]string{v1alpha1.LabelSeverity: severity},
				CreationTimestamp: metav1.NewTime(created),
			},
			Spec: v1alpha1.InvestigationSpec{
				FindingRef: v1alpha1.ObjectReference{Name: "f"}, Attempt: 1,
			},
		}
	}
	runner := &fakeRunner{}
	r, c := newInvestigation(t, runner,
		mk("inv-low", "low", clock.Add(-time.Minute)),
		mk("inv-crit", "critical", clock),
		mk("inv-high", "high", clock),
	)
	if _, err := r.Reconcile(t.Context(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "patchy", Name: schedulerRequest},
	}); err != nil {
		t.Fatalf("schedule: %v", err)
	}

	running := 0
	for _, name := range []string{"inv-low", "inv-crit", "inv-high"} {
		var inv v1alpha1.Investigation
		if err := c.Get(t.Context(), types.NamespacedName{Namespace: "patchy", Name: name}, &inv); err != nil {
			t.Fatalf("Get(%s): %v", name, err)
		}
		if inv.Status.Phase == v1alpha1.RunRunning {
			running++
			if name == "inv-low" {
				t.Error("low-severity investigation granted before critical/high")
			}
		}
	}
	if running != 2 {
		t.Errorf("running = %d, want MaxConcurrent=2 grants", running)
	}
}

func TestLaunchUsesArtifact(t *testing.T) {
	objs := investigationFixture()
	inv := objs[1].(*v1alpha1.Investigation)
	inv.Status.JobRef = nil // granted but not launched
	repo := &v1alpha1.Repository{
		ObjectMeta: metav1.ObjectMeta{Name: "finding-aa-1-src", Namespace: "patchy"},
		Spec:       v1alpha1.RepositorySpec{URL: "https://github.com/acme/orders"},
		Status: v1alpha1.RepositoryStatus{
			ResolvedSHA: "abc123",
			Artifact:    &v1alpha1.Artifact{URL: "http://arts/x.tar.gz", Digest: "deadbeef"},
		},
	}
	runner := &fakeRunner{}
	r, c := newInvestigation(t, runner, append(objs, repo)...)
	applyOnce(t, r)

	if len(runner.created) != 1 {
		t.Fatalf("jobs created = %d, want 1", len(runner.created))
	}
	spec := runner.created[0]
	if spec.ArtifactURL != "http://arts/x.tar.gz" || spec.ArtifactDigest != "deadbeef" {
		t.Errorf("artifact = %q/%q", spec.ArtifactURL, spec.ArtifactDigest)
	}
	if spec.BaseSHA != "abc123" || spec.Kind != "investigation" || spec.Finding != "finding-aa-1" {
		t.Errorf("spec = %+v", spec)
	}
	var got v1alpha1.Investigation
	key := types.NamespacedName{Namespace: "patchy", Name: "finding-aa-1-inv-1"}
	if err := c.Get(t.Context(), key, &got); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status.JobRef == nil || got.Status.JobRef.Name != "job-1" {
		t.Errorf("jobRef = %+v, want job-1", got.Status.JobRef)
	}
}
