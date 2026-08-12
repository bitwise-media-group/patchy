// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package evaluation

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/jobs"
	"github.com/bitwise-media-group/patchy/internal/kube"
	"github.com/bitwise-media-group/patchy/pkg/evaluation"
)

var clock = time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)

const digestA = "aa11bb22cc33dd44ee55ff6600112233445566778899aabbccddeeff00112233"

func unitPlan(harness string) v1alpha1.UnitPlan {
	return v1alpha1.UnitPlan{
		Skill:     "workflow-commit",
		Tier:      2,
		Model:     "anthropic/claude-sonnet-5",
		Harnesses: []v1alpha1.HarnessOption{{Harness: harness}},
		Workspace: v1alpha1.WorkspaceRef{Digest: digestA, SizeBytes: 2048},
		ExecJSON:  `{"skill":"workflow-commit","tier":2}`,
	}
}

func testEval(units ...v1alpha1.UnitPlan) *v1alpha1.Evaluation {
	return &v1alpha1.Evaluation{
		ObjectMeta: metav1.ObjectMeta{Name: "eval-1", Namespace: "patchy", UID: "eval-uid-1"},
		Spec:       v1alpha1.EvaluationSpec{Submitter: "dev@example.com", Units: units},
	}
}

func newClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	return fake.NewClientBuilder().
		WithScheme(kube.Scheme()).
		WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.Evaluation{}, &v1alpha1.EvaluationUnit{}).
		Build()
}

// fakeRunner is a canned jobs surface.
type fakeRunner struct {
	created []jobs.EvalSpec
	status  map[string]jobs.Status
	lines   map[string][]string
	deleted []string
	gone    map[string]bool
}

func (f *fakeRunner) CreateEval(_ context.Context, spec jobs.EvalSpec) (string, error) {
	f.created = append(f.created, spec)
	return jobs.EvalNameFor(spec.Unit), nil
}

func (f *fakeRunner) Status(_ context.Context, jobName string) (jobs.Status, error) {
	if f.gone[jobName] {
		return jobs.Status{}, kerrors.NewNotFound(schema.GroupResource{Group: "batch", Resource: "jobs"}, jobName)
	}
	return f.status[jobName], nil
}

func (f *fakeRunner) ResultLines(_ context.Context, jobName string, fn func([]byte) error) error {
	for _, line := range f.lines[jobName] {
		if err := fn([]byte(line)); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeRunner) Delete(_ context.Context, jobName string) error {
	f.deleted = append(f.deleted, jobName)
	return nil
}

type fakeWorkspaces struct{ present map[string]bool }

func (f fakeWorkspaces) Stat(_ context.Context, digest string) (bool, error) {
	return f.present[digest], nil
}

func newUnitReconciler(c client.Client, runner *fakeRunner, ws fakeWorkspaces) *UnitReconciler {
	return &UnitReconciler{
		Client:           c,
		Runner:           runner,
		Workspaces:       ws,
		Namespace:        "patchy",
		MaxConcurrent:    2,
		EnabledHarnesses: []string{"claude", "fake"},
		ArtifactBaseURL:  "http://arts.local:9790",
		Now:              func() time.Time { return clock },
	}
}

func reconcileUnit(t *testing.T, r *UnitReconciler, name string) ctrl.Result {
	t.Helper()
	res, err := r.Reconcile(t.Context(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "patchy", Name: name},
	})
	if err != nil {
		t.Fatalf("Reconcile(%s): %v", name, err)
	}
	return res
}

func getUnit(t *testing.T, c client.Client, name string) *v1alpha1.EvaluationUnit {
	t.Helper()
	var u v1alpha1.EvaluationUnit
	if err := c.Get(t.Context(), types.NamespacedName{Namespace: "patchy", Name: name}, &u); err != nil {
		t.Fatalf("Get(%s): %v", name, err)
	}
	return &u
}

func getEval(t *testing.T, c client.Client, name string) *v1alpha1.Evaluation {
	t.Helper()
	var e v1alpha1.Evaluation
	if err := c.Get(t.Context(), types.NamespacedName{Namespace: "patchy", Name: name}, &e); err != nil {
		t.Fatalf("Get(%s): %v", name, err)
	}
	return &e
}

func resultLine(t *testing.T, res *evaluation.UnitResult) string {
	t.Helper()
	line, err := (evaluation.Event{
		Type:   evaluation.TypeResult,
		Unit:   &evaluation.UnitRef{Skill: "workflow-commit", Key: "anthropic/claude-sonnet-5", Kind: "evals"},
		Result: res,
	}).Encode()
	if err != nil {
		t.Fatalf("encode result: %v", err)
	}
	return line
}

func TestGateCreatesChildrenIdempotently(t *testing.T) {
	eval := testEval(unitPlan("claude"), unitPlan("claude"))
	c := newClient(t, eval)
	g := &GateReconciler{Client: c, Namespace: "patchy",
		EnabledHarnesses: []string{"claude"}, Now: func() time.Time { return clock }}

	for range 2 { // twice: idempotent
		if _, err := g.Reconcile(t.Context(), ctrl.Request{
			NamespacedName: types.NamespacedName{Namespace: "patchy", Name: "eval-1"},
		}); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
	}

	var units v1alpha1.EvaluationUnitList
	if err := c.List(t.Context(), &units, client.InNamespace("patchy")); err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(units.Items) != 2 {
		t.Fatalf("units = %d, want 2", len(units.Items))
	}
	for i := range units.Items {
		u := &units.Items[i]
		if len(u.OwnerReferences) != 1 || u.OwnerReferences[0].Name != "eval-1" {
			t.Errorf("unit %s lacks the owner reference", u.Name)
		}
		if len(u.Finalizers) != 1 || u.Finalizers[0] != v1alpha1.FinalizerJobs {
			t.Errorf("unit %s lacks the jobs finalizer", u.Name)
		}
	}

	parent := getEval(t, c, "eval-1")
	if parent.Status.Phase != v1alpha1.EvaluationRunning {
		t.Errorf("parent phase = %q, want Running", parent.Status.Phase)
	}
	if !meta.IsStatusConditionTrue(parent.Status.Conditions, v1alpha1.ConditionUnitsCreated) {
		t.Error("UnitsCreated condition not true")
	}
	if parent.Status.Units != 2 {
		t.Errorf("parent units = %d, want 2", parent.Status.Units)
	}
}

func TestGateSettlesHarnessUnavailable(t *testing.T) {
	eval := testEval(unitPlan("gemini")) // not in the fleet
	c := newClient(t, eval)
	g := &GateReconciler{Client: c, Namespace: "patchy",
		EnabledHarnesses: []string{"claude"}, Now: func() time.Time { return clock }}

	if _, err := g.Reconcile(t.Context(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "patchy", Name: "eval-1"},
	}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	u := getUnit(t, c, "eval-1-u000")
	if u.Status.Phase != v1alpha1.RunFailed || u.Status.Reason != v1alpha1.UnitHarnessUnavailable {
		t.Errorf("unit = %s/%s, want Failed/HarnessUnavailable", u.Status.Phase, u.Status.Reason)
	}
}

func TestSchedulerRespectsSlots(t *testing.T) {
	eval := testEval(unitPlan("claude"), unitPlan("claude"), unitPlan("claude"))
	objs := make([]client.Object, 0, 4)
	objs = append(objs, eval)
	for i := range 3 {
		objs = append(objs, &v1alpha1.EvaluationUnit{
			ObjectMeta: metav1.ObjectMeta{
				Name: UnitName("eval-1", i), Namespace: "patchy",
				Labels:            map[string]string{v1alpha1.LabelEvaluation: "eval-1"},
				CreationTimestamp: metav1.NewTime(clock.Add(time.Duration(i) * time.Second)),
			},
			Spec: v1alpha1.EvaluationUnitSpec{
				EvaluationRef: v1alpha1.ObjectReference{Name: "eval-1", UID: "eval-uid-1"},
				Index:         int32(i),
				Unit:          unitPlan("claude"),
			},
		})
	}
	c := newClient(t, objs...)
	r := newUnitReconciler(c, &fakeRunner{}, fakeWorkspaces{present: map[string]bool{digestA: true}})

	if _, err := r.schedule(t.Context()); err != nil {
		t.Fatalf("schedule: %v", err)
	}

	granted := 0
	for i := range 3 {
		if getUnit(t, c, UnitName("eval-1", i)).Status.Phase == v1alpha1.RunRunning {
			granted++
		}
	}
	if granted != 2 {
		t.Errorf("granted = %d, want 2 (MaxConcurrent)", granted)
	}
	// FIFO: the two earliest-created units get the slots.
	if getUnit(t, c, "eval-1-u002").Status.Phase == v1alpha1.RunRunning {
		t.Error("newest unit granted before older ones")
	}
}

func runningUnit(jobRef *v1alpha1.JobReference) *v1alpha1.EvaluationUnit {
	const index = int32(0)
	u := &v1alpha1.EvaluationUnit{
		ObjectMeta: metav1.ObjectMeta{
			Name: UnitName("eval-1", int(index)), Namespace: "patchy",
			Labels:     map[string]string{v1alpha1.LabelEvaluation: "eval-1"},
			Finalizers: []string{v1alpha1.FinalizerJobs},
		},
		Spec: v1alpha1.EvaluationUnitSpec{
			EvaluationRef: v1alpha1.ObjectReference{Name: "eval-1", UID: "eval-uid-1"},
			Index:         index,
			Unit:          unitPlan("claude"),
		},
		Status: v1alpha1.EvaluationUnitStatus{Phase: v1alpha1.RunRunning, JobRef: jobRef},
	}
	return u
}

func TestLaunchCreatesJobAndStampsRef(t *testing.T) {
	eval := testEval(unitPlan("claude"))
	unit := runningUnit(nil)
	c := newClient(t, eval, unit)
	runner := &fakeRunner{}
	r := newUnitReconciler(c, runner, fakeWorkspaces{present: map[string]bool{digestA: true}})

	reconcileUnit(t, r, unit.Name)

	if len(runner.created) != 1 {
		t.Fatalf("created jobs = %d, want 1", len(runner.created))
	}
	spec := runner.created[0]
	if spec.Harness != "claude" || spec.Evaluation != "eval-1" || spec.Unit != unit.Name {
		t.Errorf("job spec = %+v", spec)
	}
	if !strings.Contains(spec.ArtifactURL, digestA) {
		t.Errorf("artifact URL %q lacks the digest", spec.ArtifactURL)
	}
	u := getUnit(t, c, unit.Name)
	if u.Status.JobRef == nil || u.Status.Harness != "claude" || u.Status.StartedAt == nil {
		t.Errorf("unit status not stamped: %+v", u.Status)
	}
}

func TestLaunchWorkspaceLost(t *testing.T) {
	eval := testEval(unitPlan("claude"))
	unit := runningUnit(nil)
	c := newClient(t, eval, unit)
	r := newUnitReconciler(c, &fakeRunner{}, fakeWorkspaces{present: map[string]bool{}})

	reconcileUnit(t, r, unit.Name)

	u := getUnit(t, c, unit.Name)
	if u.Status.Phase != v1alpha1.RunFailed || u.Status.Reason != v1alpha1.UnitWorkspaceLost {
		t.Errorf("unit = %s/%s, want Failed/WorkspaceLost", u.Status.Phase, u.Status.Reason)
	}
	parent := getEval(t, c, "eval-1")
	if !meta.IsStatusConditionTrue(parent.Status.Conditions, v1alpha1.ConditionStalled) {
		t.Error("parent Stalled condition not set")
	}
	// Single unit settled → parent terminal Failed.
	if parent.Status.Phase != v1alpha1.EvaluationFailed {
		t.Errorf("parent phase = %q, want Failed", parent.Status.Phase)
	}
}

func TestCollectResultOK(t *testing.T) {
	eval := testEval(unitPlan("claude"))
	unit := runningUnit(nil)
	jobName := jobs.EvalNameFor(unit.Name)
	unit.Status.JobRef = &v1alpha1.JobReference{Name: jobName}
	c := newClient(t, eval, unit)

	runner := &fakeRunner{
		status: map[string]jobs.Status{jobName: {Succeeded: 1, Done: true}},
		lines: map[string][]string{jobName: {
			"stderr chatter",
			resultLine(t, &evaluation.UnitResult{
				Tier: 2, Model: "anthropic/claude-sonnet-5", Harness: "claude",
				Summary: evaluation.ResultSummary{
					CasesPassed: 3, CasesFailed: 1,
					Cases: []evaluation.CaseStatus{
						{ID: "a", Passed: true}, {ID: "b", Passed: true},
						{ID: "c", Passed: true}, {ID: "d", Passed: false},
					},
					TokenUsage: evaluation.TokenUsage{InputTokens: 1000, OutputTokens: 200, CostUSD: 0.75},
					ElapsedMS:  90000,
					Outcome:    "ok",
				},
				Entry: []byte(`{"schema":5,"results":[{"id":"a"}]}`),
			}),
		}},
	}
	r := newUnitReconciler(c, runner, fakeWorkspaces{present: map[string]bool{digestA: true}})

	reconcileUnit(t, r, unit.Name)

	u := getUnit(t, c, unit.Name)
	if u.Status.Phase != v1alpha1.RunComplete {
		t.Fatalf("phase = %q, want Complete", u.Status.Phase)
	}
	if u.Status.CasesPassed != 3 || u.Status.CasesFailed != 1 {
		t.Errorf("cases = %d/%d, want 3 passed 1 failed", u.Status.CasesPassed, u.Status.CasesFailed)
	}
	if u.Status.Usage.CostUSD != "0.750000" {
		t.Errorf("cost = %q, want 0.750000", u.Status.Usage.CostUSD)
	}
	if u.Status.ResultsRef == nil || u.Status.ResultsRef.Name != unit.Name+"-results" {
		t.Errorf("resultsRef = %+v, want %s-results", u.Status.ResultsRef, unit.Name)
	}
	parent := getEval(t, c, "eval-1")
	if parent.Status.Phase != v1alpha1.EvaluationComplete || parent.Status.UnitsComplete != 1 {
		t.Errorf("parent = %s complete=%d, want Complete/1", parent.Status.Phase, parent.Status.UnitsComplete)
	}
}

func TestCollectFatal(t *testing.T) {
	eval := testEval(unitPlan("claude"))
	unit := runningUnit(nil)
	jobName := jobs.EvalNameFor(unit.Name)
	unit.Status.JobRef = &v1alpha1.JobReference{Name: jobName}
	c := newClient(t, eval, unit)

	fatalLine, err := (evaluation.Event{Type: evaluation.TypeFatal, Error: "bundle missing evals dir"}).Encode()
	if err != nil {
		t.Fatalf("encode fatal: %v", err)
	}
	runner := &fakeRunner{
		status: map[string]jobs.Status{jobName: {Failed: 1, Done: true}},
		lines:  map[string][]string{jobName: {fatalLine}},
	}
	r := newUnitReconciler(c, runner, fakeWorkspaces{present: map[string]bool{digestA: true}})

	reconcileUnit(t, r, unit.Name)

	u := getUnit(t, c, unit.Name)
	if u.Status.Phase != v1alpha1.RunFailed || u.Status.Reason != v1alpha1.UnitJobFailed {
		t.Errorf("unit = %s/%s, want Failed/JobFailed", u.Status.Phase, u.Status.Reason)
	}
	if !strings.Contains(u.Status.Detail, "bundle missing") {
		t.Errorf("detail = %q, want the fatal error", u.Status.Detail)
	}
}

func TestCollectNoResult(t *testing.T) {
	eval := testEval(unitPlan("claude"))
	unit := runningUnit(nil)
	jobName := jobs.EvalNameFor(unit.Name)
	unit.Status.JobRef = &v1alpha1.JobReference{Name: jobName}
	c := newClient(t, eval, unit)

	runner := &fakeRunner{
		status: map[string]jobs.Status{jobName: {Failed: 1, Done: true}},
		lines:  map[string][]string{jobName: {"no events at all"}},
	}
	r := newUnitReconciler(c, runner, fakeWorkspaces{present: map[string]bool{digestA: true}})

	reconcileUnit(t, r, unit.Name)

	u := getUnit(t, c, unit.Name)
	if u.Status.Phase != v1alpha1.RunFailed || u.Status.Reason != v1alpha1.UnitJobFailed {
		t.Errorf("unit = %s/%s, want Failed/JobFailed", u.Status.Phase, u.Status.Reason)
	}
}

func TestCollectOversizeEntry(t *testing.T) {
	eval := testEval(unitPlan("claude"))
	unit := runningUnit(nil)
	jobName := jobs.EvalNameFor(unit.Name)
	unit.Status.JobRef = &v1alpha1.JobReference{Name: jobName}
	c := newClient(t, eval, unit)

	big := fmt.Sprintf(`{"pad":%q}`, strings.Repeat("x", evaluation.MaxEntryBytes))
	runner := &fakeRunner{
		status: map[string]jobs.Status{jobName: {Succeeded: 1, Done: true}},
		lines: map[string][]string{jobName: {resultLine(t, &evaluation.UnitResult{
			Tier: 2, Model: "anthropic/claude-sonnet-5", Harness: "claude",
			Summary: evaluation.ResultSummary{CasesPassed: 1, Outcome: "ok"},
			Entry:   []byte(big),
		})}},
	}
	r := newUnitReconciler(c, runner, fakeWorkspaces{present: map[string]bool{digestA: true}})

	reconcileUnit(t, r, unit.Name)

	u := getUnit(t, c, unit.Name)
	if u.Status.Phase != v1alpha1.RunFailed || u.Status.Reason != v1alpha1.UnitResultTooLarge {
		t.Errorf("unit = %s/%s, want Failed/ResultTooLarge", u.Status.Phase, u.Status.Reason)
	}
	// The bounded summary still landed.
	if u.Status.CasesPassed != 1 {
		t.Errorf("casesPassed = %d, want 1 despite oversize entry", u.Status.CasesPassed)
	}
	if u.Status.ResultsRef != nil {
		t.Error("resultsRef set despite oversize entry")
	}
}

func TestFinalizeDeletesJobAndReleasesFinalizer(t *testing.T) {
	eval := testEval(unitPlan("claude"))
	unit := runningUnit(&v1alpha1.JobReference{Name: jobs.EvalNameFor("eval-1-u000")})
	now := metav1.NewTime(clock)
	unit.DeletionTimestamp = &now
	c := newClient(t, eval, unit)
	runner := &fakeRunner{}
	r := newUnitReconciler(c, runner, fakeWorkspaces{present: map[string]bool{digestA: true}})

	reconcileUnit(t, r, unit.Name)

	if len(runner.deleted) != 1 || runner.deleted[0] != jobs.EvalNameFor(unit.Name) {
		t.Errorf("deleted jobs = %v, want the unit's job", runner.deleted)
	}
	// Releasing the sole finalizer lets the fake client delete the object.
	var u v1alpha1.EvaluationUnit
	err := c.Get(t.Context(), types.NamespacedName{Namespace: "patchy", Name: unit.Name}, &u)
	if !kerrors.IsNotFound(err) {
		t.Errorf("unit still present after finalize: %v", err)
	}
}

func TestTTLDeletesExpired(t *testing.T) {
	eval := testEval(unitPlan("claude"))
	done := metav1.NewTime(clock.Add(-100 * time.Hour))
	eval.Status.Phase = v1alpha1.EvaluationComplete
	eval.Status.CompletedAt = &done
	c := newClient(t, eval)
	r := &TTLReconciler{Client: c, Namespace: "patchy", TTL: -1, Now: func() time.Time { return clock }}

	if _, err := r.Reconcile(t.Context(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "patchy", Name: "eval-1"},
	}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	var e v1alpha1.Evaluation
	err := c.Get(t.Context(), types.NamespacedName{Namespace: "patchy", Name: "eval-1"}, &e)
	if !kerrors.IsNotFound(err) {
		t.Errorf("evaluation still present after TTL: %v", err)
	}
}

func TestTTLWaitsAndHonorsSpecOverride(t *testing.T) {
	eval := testEval(unitPlan("claude"))
	done := metav1.NewTime(clock.Add(-time.Hour))
	eval.Status.Phase = v1alpha1.EvaluationComplete
	eval.Status.CompletedAt = &done
	ttl := int32(7200) // 2h override; 1h elapsed
	eval.Spec.TTLSecondsAfterFinished = &ttl
	c := newClient(t, eval)
	r := &TTLReconciler{Client: c, Namespace: "patchy", TTL: -1, Now: func() time.Time { return clock }}

	res, err := r.Reconcile(t.Context(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "patchy", Name: "eval-1"},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.RequeueAfter != time.Hour {
		t.Errorf("RequeueAfter = %v, want 1h", res.RequeueAfter)
	}
	var e v1alpha1.Evaluation
	if err := c.Get(t.Context(), types.NamespacedName{Namespace: "patchy", Name: "eval-1"}, &e); err != nil {
		t.Errorf("evaluation deleted early: %v", err)
	}
}
