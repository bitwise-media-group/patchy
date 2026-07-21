// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package remedctrl

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/envelope"
	"github.com/bitwise-media-group/patchy/internal/jobs"
	"github.com/bitwise-media-group/patchy/internal/kube"
)

var crdClock = time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)

// queuedFinding is investigated and queued for remediation.
func queuedFinding(phase v1alpha1.Phase) *v1alpha1.Finding {
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
			Phase: phase,
			Attempts: v1alpha1.AttemptCounts{
				Investigation: 1,
			},
			Investigation: &v1alpha1.InvestigationSummary{
				Name: "finding-aa-1-inv-1", Attempt: 1, Outcome: "ok",
				Recommendation: v1alpha1.RecommendationRemediate,
				Exploitability: v1alpha1.RatingHigh,
				Likelihood:     v1alpha1.RatingMedium,
				Impact:         v1alpha1.RatingCritical,
			},
			Tracking: &v1alpha1.TrackingStatus{Integration: "gh", IssueNumber: 7},
		},
	}
}

func invChild() *v1alpha1.Investigation {
	return &v1alpha1.Investigation{
		ObjectMeta: metav1.ObjectMeta{Name: "finding-aa-1-inv-1", Namespace: "patchy"},
		Spec: v1alpha1.InvestigationSpec{
			FindingRef: v1alpha1.ObjectReference{Name: "finding-aa-1", UID: "uid-1"}, Attempt: 1,
		},
		Status: v1alpha1.InvestigationStatus{
			Phase:  v1alpha1.RunComplete,
			Report: "---\ninvestigation frontmatter\n---\nanalysis",
			RemediationParameters: &v1alpha1.AgentParameters{
				Model: "claude-sonnet-5", MaxTurns: 40, TokenBudget: 200000,
			},
		},
	}
}

func newSpawner(t *testing.T, objs ...client.Object) (*SpawnerReconciler, client.Client) {
	t.Helper()
	c := fake.NewClientBuilder().
		WithScheme(kube.Scheme()).
		WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.Finding{}, &v1alpha1.Remediation{}).
		Build()
	return &SpawnerReconciler{
		Client:    c,
		Namespace: "patchy",
		Now:       func() time.Time { return crdClock },
	}, c
}

func spawnOnce(t *testing.T, r *SpawnerReconciler) {
	t.Helper()
	if _, err := r.Reconcile(t.Context(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "patchy", Name: "finding-aa-1"},
	}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
}

func findingNow(t *testing.T, c client.Client) *v1alpha1.Finding {
	t.Helper()
	var f v1alpha1.Finding
	if err := c.Get(t.Context(), types.NamespacedName{Namespace: "patchy", Name: "finding-aa-1"}, &f); err != nil {
		t.Fatalf("Get finding: %v", err)
	}
	return &f
}

func TestSpawnerCreatesRemediation(t *testing.T) {
	r, c := newSpawner(t, queuedFinding(v1alpha1.PhaseQueued), invChild())
	spawnOnce(t, r)

	var rem v1alpha1.Remediation
	key := types.NamespacedName{Namespace: "patchy", Name: "finding-aa-1-rem-1"}
	if err := c.Get(t.Context(), key, &rem); err != nil {
		t.Fatalf("Remediation not created: %v", err)
	}
	// severity high (0.3*2) + expl high (0.3*2.25) + like medium (0.2*1.5) +
	// impact critical (0.2*3) = 2.175 / 3 * 100 = 72.5 → 73
	if rem.Spec.Priority != 73 {
		t.Errorf("priority = %d, want 73", rem.Spec.Priority)
	}
	if rem.Spec.Parameters.MaxTurns != 40 {
		t.Errorf("parameters = %+v, want investigation's clamped suggestion", rem.Spec.Parameters)
	}
	if rem.Spec.InvestigationRef.Name != "finding-aa-1-inv-1" {
		t.Errorf("investigationRef = %+v", rem.Spec.InvestigationRef)
	}
	if got := findingNow(t, c).Status.Attempts.Remediation; got != 1 {
		t.Errorf("attempts.remediation = %d, want 1", got)
	}

	// Second pass is a no-op (attempt exists).
	spawnOnce(t, r)
	var list v1alpha1.RemediationList
	if err := c.List(t.Context(), &list, client.InNamespace("patchy")); err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list.Items) != 1 {
		t.Errorf("remediations = %d after re-reconcile, want 1", len(list.Items))
	}
}

func TestSpawnerAdmitsApproval(t *testing.T) {
	fnd := queuedFinding(v1alpha1.PhaseAwaitingApproval)
	fnd.Spec.Approval = &v1alpha1.Approval{By: "alice", At: metav1.NewTime(crdClock)}
	r, c := newSpawner(t, fnd, invChild())
	spawnOnce(t, r)
	if got := findingNow(t, c).Status.Phase; got != v1alpha1.PhaseQueued {
		t.Errorf("phase = %q, want Queued (edge 10)", got)
	}
}

func TestSpawnerRevivesHandedOff(t *testing.T) {
	fnd := queuedFinding(v1alpha1.PhaseHandedOff)
	done := metav1.NewTime(crdClock.Add(-time.Hour))
	fnd.Status.CompletedAt = &done
	fnd.Spec.Approval = &v1alpha1.Approval{By: "alice", At: metav1.NewTime(crdClock)}
	r, c := newSpawner(t, fnd, invChild())
	spawnOnce(t, r)
	f := findingNow(t, c)
	if f.Status.Phase != v1alpha1.PhaseQueued {
		t.Errorf("phase = %q, want Queued (edge 18)", f.Status.Phase)
	}
	if f.Status.CompletedAt != nil {
		t.Error("completedAt not cleared on revival — TTL would still fire")
	}
}

func TestSpawnerIgnoresStaleApprovalOnHandedOff(t *testing.T) {
	fnd := queuedFinding(v1alpha1.PhaseHandedOff)
	done := metav1.NewTime(crdClock)
	fnd.Status.CompletedAt = &done
	fnd.Spec.Approval = &v1alpha1.Approval{By: "alice", At: metav1.NewTime(crdClock.Add(-2 * time.Hour))}
	r, c := newSpawner(t, fnd, invChild())
	spawnOnce(t, r)
	if got := findingNow(t, c).Status.Phase; got != v1alpha1.PhaseHandedOff {
		t.Errorf("phase = %q, want HandedOff (stale approval predates hand-off)", got)
	}
}

// fakeCRRunner fakes the jobs seam.
type fakeCRRunner struct {
	created []jobs.Spec
	done    bool
	events  []envelope.Event
}

func (f *fakeCRRunner) Create(_ context.Context, spec jobs.Spec) (string, error) {
	f.created = append(f.created, spec)
	return "job-rem-1", nil
}

func (f *fakeCRRunner) Result(context.Context, string) ([]envelope.Event, error) {
	return f.events, nil
}

func (f *fakeCRRunner) Status(context.Context, string) (jobs.Status, error) {
	st := jobs.Status{Done: f.done}
	if f.done {
		st.Succeeded = 1
	}
	return st, nil
}

func (f *fakeCRRunner) Delete(context.Context, string) error { return nil }

// fakeForge records pushes and PRs.
type fakeForge struct {
	pushed  []string // branches
	prs     []string // branches
	prCalls int
}

func (f *fakeForge) Push(_ context.Context, _, _, branch string, cs *envelope.Changeset) error {
	if cs == nil {
		panic("push without changeset")
	}
	f.pushed = append(f.pushed, branch)
	return nil
}

func (f *fakeForge) EnsurePR(_ context.Context, _, _, branch, _, _ string) (int64, string, error) {
	f.prCalls++
	f.prs = append(f.prs, branch)
	return 11, "https://github.com/acme/orders/pull/11", nil
}

// runningRemediation is a granted Remediation with its finding + artifacts.
func runningRemediation() []client.Object {
	fnd := queuedFinding(v1alpha1.PhaseRemediating)
	fnd.Status.Attempts.Remediation = 1
	rem := &v1alpha1.Remediation{
		ObjectMeta: metav1.ObjectMeta{
			Name: "finding-aa-1-rem-1", Namespace: "patchy",
			Labels: map[string]string{v1alpha1.LabelFinding: "finding-aa-1"},
		},
		Spec: v1alpha1.RemediationSpec{
			FindingRef:       v1alpha1.ObjectReference{Name: "finding-aa-1", UID: "uid-1"},
			InvestigationRef: v1alpha1.ObjectReference{Name: "finding-aa-1-inv-1"},
			RepositoryRef:    v1alpha1.LocalObjectReference{Name: "finding-aa-1-src"},
			Attempt:          1,
			Priority:         83,
		},
		Status: v1alpha1.RemediationStatus{
			Phase:  v1alpha1.RunRunning,
			JobRef: &v1alpha1.JobReference{Name: "job-rem-1"},
		},
	}
	repo := &v1alpha1.Repository{
		ObjectMeta: metav1.ObjectMeta{Name: "finding-aa-1-src", Namespace: "patchy"},
		Spec:       v1alpha1.RepositorySpec{URL: "https://github.com/acme/orders"},
		Status: v1alpha1.RepositoryStatus{
			ResolvedSHA: "abc123",
			Artifact:    &v1alpha1.Artifact{URL: "http://arts/x.tar.gz", Digest: "d"},
		},
	}
	return []client.Object{fnd, rem, invChild(), repo}
}

func newRemediation(
	t *testing.T, runner *fakeCRRunner, fw *fakeForge, objs ...client.Object,
) (*RemediationReconciler, client.Client) {
	t.Helper()
	c := fake.NewClientBuilder().
		WithScheme(kube.Scheme()).
		WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.Finding{}, &v1alpha1.Remediation{}).
		Build()
	return &RemediationReconciler{
		Client:        c,
		Runner:        runner,
		Forge:         fw,
		Namespace:     "patchy",
		MaxConcurrent: 1,
		MaxAttempts:   2,
		Now:           func() time.Time { return crdClock },
	}, c
}

func remOnce(t *testing.T, r *RemediationReconciler, name string) {
	t.Helper()
	if _, err := r.Reconcile(t.Context(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "patchy", Name: name},
	}); err != nil {
		t.Fatalf("Reconcile(%s): %v", name, err)
	}
}

func crdRemediationEvent(success bool) []envelope.Event {
	return []envelope.Event{{
		V: envelope.Version, Type: envelope.TypeRemediation, Finding: "finding-aa-1",
		Remediation: &envelope.Remediation{
			Stage: envelope.Stage{Outcome: envelope.OutcomeOK, Harness: "claude",
				Model: "claude-sonnet-5", Usage: envelope.Usage{CostUSD: 3.5}},
			ReportMarkdown: "fix report",
			Success:        success,
			Confidence:     0.9,
			Branch:         "patchy/finding-aa-1",
			Changeset: &envelope.Changeset{
				BaseSHA:       "abc123",
				CommitMessage: "fix",
				Upserts:       []envelope.FileChange{{Path: "a.go", Mode: "100644", ContentB64: "eA=="}},
			},
		},
	}}
}

func TestRemediationSuccessPushesAndOpensPR(t *testing.T) {
	runner := &fakeCRRunner{done: true, events: crdRemediationEvent(true)}
	fw := &fakeForge{}
	r, c := newRemediation(t, runner, fw, runningRemediation()...)
	remOnce(t, r, "finding-aa-1-rem-1")

	if len(fw.pushed) != 1 || fw.pushed[0] != "patchy/finding-aa-1" {
		t.Errorf("pushed = %v, want patchy/finding-aa-1", fw.pushed)
	}
	if fw.prCalls != 1 {
		t.Errorf("prCalls = %d, want 1", fw.prCalls)
	}
	f := findingNow(t, c)
	if f.Status.Phase != v1alpha1.PhaseInReview {
		t.Errorf("phase = %q, want InReview (edge 13)", f.Status.Phase)
	}
	if f.Status.PullRequest == nil || f.Status.PullRequest.Number != 11 || f.Status.PullRequest.State != "open" {
		t.Errorf("pullRequest = %+v", f.Status.PullRequest)
	}
	var rem v1alpha1.Remediation
	key := types.NamespacedName{Namespace: "patchy", Name: "finding-aa-1-rem-1"}
	if err := c.Get(t.Context(), key, &rem); err != nil {
		t.Fatalf("Get remediation: %v", err)
	}
	if !rem.Status.Success || rem.Status.Branch != "patchy/finding-aa-1" || rem.Status.PullRequest == nil {
		t.Errorf("remediation status = %+v", rem.Status)
	}
	if rem.Status.Stage == nil || rem.Status.Stage.Usage.CostUSD != "3.500000" {
		t.Errorf("stage = %+v, want cost 3.500000", rem.Status.Stage)
	}
}

func TestRemediationUnfixableHandsOff(t *testing.T) {
	events := crdRemediationEvent(false)
	events[0].Remediation.Changeset = nil
	runner := &fakeCRRunner{done: true, events: events}
	fw := &fakeForge{}
	r, c := newRemediation(t, runner, fw, runningRemediation()...)
	remOnce(t, r, "finding-aa-1-rem-1")

	if len(fw.pushed) != 0 {
		t.Errorf("pushed = %v, want none for unfixable", fw.pushed)
	}
	if got := findingNow(t, c).Status.Phase; got != v1alpha1.PhaseHandedOff {
		t.Errorf("phase = %q, want HandedOff (edge 14)", got)
	}
}

func TestRemediationFailureRequeuesThenExhausts(t *testing.T) {
	failEvents := []envelope.Event{{
		V: envelope.Version, Type: envelope.TypeRemediation,
		Remediation: &envelope.Remediation{Stage: envelope.Stage{
			Outcome: envelope.OutcomeTimeout, Detail: "wall clock",
		}},
	}}
	runner := &fakeCRRunner{done: true, events: failEvents}
	r, c := newRemediation(t, runner, &fakeForge{}, runningRemediation()...)
	remOnce(t, r, "finding-aa-1-rem-1")
	if got := findingNow(t, c).Status.Phase; got != v1alpha1.PhaseQueued {
		t.Fatalf("phase = %q after attempt 1, want Queued (edge 12)", got)
	}

	objs := runningRemediation()
	rem := objs[1].(*v1alpha1.Remediation)
	rem.Name = "finding-aa-1-rem-2"
	rem.Spec = v1alpha1.RemediationSpec{
		FindingRef:       v1alpha1.ObjectReference{Name: "finding-aa-1", UID: "uid-1"},
		InvestigationRef: v1alpha1.ObjectReference{Name: "finding-aa-1-inv-1"},
		RepositoryRef:    v1alpha1.LocalObjectReference{Name: "finding-aa-1-src"},
		Attempt:          2, Priority: 83,
	}
	r2, c2 := newRemediation(t, runner, &fakeForge{}, objs...)
	remOnce(t, r2, "finding-aa-1-rem-2")
	var f v1alpha1.Finding
	if err := c2.Get(t.Context(), types.NamespacedName{Namespace: "patchy", Name: "finding-aa-1"}, &f); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if f.Status.Phase != v1alpha1.PhaseFailed {
		t.Errorf("phase = %q after exhausted attempts, want Failed (edge 15)", f.Status.Phase)
	}
}

func TestRemediationSchedulerPriorityOrder(t *testing.T) {
	mk := func(name string, prio int32) *v1alpha1.Remediation {
		return &v1alpha1.Remediation{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: "patchy",
				CreationTimestamp: metav1.NewTime(crdClock),
			},
			Spec: v1alpha1.RemediationSpec{
				FindingRef:       v1alpha1.ObjectReference{Name: "f-" + name},
				InvestigationRef: v1alpha1.ObjectReference{Name: "i"},
				RepositoryRef:    v1alpha1.LocalObjectReference{Name: "s"},
				Attempt:          1, Priority: prio,
			},
		}
	}
	runner := &fakeCRRunner{}
	r, c := newRemediation(t, runner, &fakeForge{}, mk("rem-low", 10), mk("rem-high", 90))
	if _, err := r.Reconcile(t.Context(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "patchy", Name: remSchedulerRequest},
	}); err != nil {
		t.Fatalf("schedule: %v", err)
	}

	var high, low v1alpha1.Remediation
	if err := c.Get(t.Context(), types.NamespacedName{Namespace: "patchy", Name: "rem-high"}, &high); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if err := c.Get(t.Context(), types.NamespacedName{Namespace: "patchy", Name: "rem-low"}, &low); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if high.Status.Phase != v1alpha1.RunRunning {
		t.Errorf("high-priority phase = %q, want Running", high.Status.Phase)
	}
	if low.Status.Phase == v1alpha1.RunRunning {
		t.Error("low-priority granted despite MaxConcurrent=1")
	}
}

func TestRemediationLaunchCarriesInvestigationReport(t *testing.T) {
	objs := runningRemediation()
	rem := objs[1].(*v1alpha1.Remediation)
	rem.Status.JobRef = nil // granted, not launched
	runner := &fakeCRRunner{}
	r, _ := newRemediation(t, runner, &fakeForge{}, objs...)
	remOnce(t, r, "finding-aa-1-rem-1")

	if len(runner.created) != 1 {
		t.Fatalf("jobs = %d, want 1", len(runner.created))
	}
	spec := runner.created[0]
	if spec.Kind != "remediation" || spec.Phase != "remediate" {
		t.Errorf("spec kind/phase = %q/%q", spec.Kind, spec.Phase)
	}
	if spec.InvestigationMarkdown == "" {
		t.Error("investigation report not handed to the remediation pod")
	}
	if spec.Token != "" {
		t.Error("token present — split-pipeline jobs must be credential-less")
	}
}
