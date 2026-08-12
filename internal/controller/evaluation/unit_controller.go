// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package evaluation

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	"github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/agentresult"
	"github.com/bitwise-media-group/patchy/internal/evalresults"
	"github.com/bitwise-media-group/patchy/internal/jobs"
	"github.com/bitwise-media-group/patchy/internal/schedule"
	"github.com/bitwise-media-group/patchy/pkg/evaluation"
)

// schedulerRequest is the singleton request every watch event maps to — one
// serialized decision point; the controller's default MaxConcurrentReconciles
// of 1 provides the mutex.
const schedulerRequest = "\x00scheduler"

// Runner is the jobs surface the unit reconciler needs; satisfied by
// *jobs.Client.
type Runner interface {
	CreateEval(ctx context.Context, spec jobs.EvalSpec) (string, error)
	ResultLines(ctx context.Context, jobName string, fn func(line []byte) error) error
	Status(ctx context.Context, jobName string) (jobs.Status, error)
	Delete(ctx context.Context, jobName string) error
}

// WorkspaceStat checks a workspace blob's presence before launch; satisfied
// by *artifact.Client.
type WorkspaceStat interface {
	Stat(ctx context.Context, digest string) (bool, error)
}

// evuGVK is the owner TypeMeta stamped on results ConfigMaps.
var evuGVK = metav1.TypeMeta{
	APIVersion: v1alpha1.GroupVersion.String(),
	Kind:       "EvaluationUnit",
}

// UnitReconciler schedules EvaluationUnits into agent Jobs with bounded
// concurrency and collects their results. Slot accounting comes from the
// cluster, never memory.
type UnitReconciler struct {
	client.Client
	Runner     Runner
	Workspaces WorkspaceStat
	Namespace  string
	// MaxConcurrent bounds simultaneously Running units (default 4).
	MaxConcurrent int
	// EnabledHarnesses is the evolve-runner fleet resolved at startup.
	EnabledHarnesses []string
	// ArtifactBaseURL is the public artifact endpoint agent pods fetch
	// bundles from, e.g. http://patchy-source-controller.patchy.svc:9790.
	ArtifactBaseURL string
	Now             func() time.Time
	Log             *slog.Logger
}

func (r *UnitReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Reconcile handles both the singleton scheduling request and per-object
// runs.
func (r *UnitReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if req.Name == schedulerRequest {
		return r.schedule(ctx)
	}
	return r.run(ctx, req)
}

// schedule grants free slots to pending units, FIFO with the unit index as
// the tiebreak (the index rides in the name). Priority is uniform — an
// evaluation batch has no severity axis — and there is no expedite.
func (r *UnitReconciler) schedule(ctx context.Context) (ctrl.Result, error) {
	var list v1alpha1.EvaluationUnitList
	if err := r.List(ctx, &list, client.InNamespace(r.Namespace)); err != nil {
		return ctrl.Result{}, err
	}
	running := 0
	var pending []schedule.Candidate
	for i := range list.Items {
		unit := &list.Items[i]
		switch unit.Status.Phase {
		case v1alpha1.RunRunning:
			running++
		case v1alpha1.RunPending, "":
			if !unit.DeletionTimestamp.IsZero() {
				continue
			}
			pending = append(pending, schedule.Candidate{
				Name:     unit.Name,
				QueuedAt: unit.CreationTimestamp.Time,
			})
		}
	}
	maxConcurrent := r.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = 4
	}
	for _, name := range schedule.Pick(pending, maxConcurrent-running, r.now(), schedule.AgingPolicy{}) {
		if err := r.grant(ctx, name); err != nil {
			return ctrl.Result{}, err
		}
	}
	// Safety tick: re-inspect even if an event is lost.
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

// grant moves one unit to Running (idempotent).
func (r *UnitReconciler) grant(ctx context.Context, name string) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var unit v1alpha1.EvaluationUnit
		if err := r.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: name}, &unit); err != nil {
			return client.IgnoreNotFound(err)
		}
		if unit.Status.Phase != "" && unit.Status.Phase != v1alpha1.RunPending {
			return nil
		}
		unit.Status.Phase = v1alpha1.RunRunning
		return r.Status().Update(ctx, &unit)
	})
}

// run dispatches one unit through launch/collect/finalize.
func (r *UnitReconciler) run(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var unit v1alpha1.EvaluationUnit
	if err := r.Get(ctx, req.NamespacedName, &unit); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !unit.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.finalize(ctx, &unit)
	}
	switch unit.Status.Phase {
	case v1alpha1.RunRunning:
		if unit.Status.JobRef == nil {
			return ctrl.Result{}, r.launch(ctx, &unit)
		}
		return r.collect(ctx, &unit)
	default:
		return ctrl.Result{}, nil // Pending waits for a grant; terminal is done
	}
}

// launch resolves the harness, checks the workspace blob, and creates the
// agent Job.
func (r *UnitReconciler) launch(ctx context.Context, unit *v1alpha1.EvaluationUnit) error {
	plan := unit.Spec.Unit
	harness := resolveHarness(plan.Harnesses, r.EnabledHarnesses)
	if harness == "" {
		return r.settle(ctx, unit, settleOutcome{
			reason: v1alpha1.UnitHarnessUnavailable,
			detail: fmt.Sprintf("no enabled harness (fleet: %v)", r.EnabledHarnesses),
		})
	}

	cached, err := r.Workspaces.Stat(ctx, plan.Workspace.Digest)
	if err != nil {
		return fmt.Errorf("stat workspace %s: %w", plan.Workspace.Digest, err)
	}
	if !cached {
		if err := r.markWorkspaceLost(ctx, unit); err != nil {
			return err
		}
		return r.settle(ctx, unit, settleOutcome{
			reason: v1alpha1.UnitWorkspaceLost,
			detail: fmt.Sprintf("workspace %s is no longer cached; re-upload and resubmit", plan.Workspace.Digest),
		})
	}

	jobName, err := r.Runner.CreateEval(ctx, jobs.EvalSpec{
		Evaluation:     unit.Spec.EvaluationRef.Name,
		Unit:           unit.Name,
		Index:          unit.Spec.Index,
		Harness:        harness,
		UnitJSON:       []byte(plan.ExecJSON),
		ArtifactURL:    strings.TrimSuffix(r.ArtifactBaseURL, "/") + "/artifacts/" + plan.Workspace.Digest + ".tar.gz",
		ArtifactDigest: plan.Workspace.Digest,
	})
	if err != nil {
		return fmt.Errorf("launch unit %s: %w", unit.Name, err)
	}

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var cur v1alpha1.EvaluationUnit
		if err := r.Get(ctx, client.ObjectKeyFromObject(unit), &cur); err != nil {
			return client.IgnoreNotFound(err)
		}
		if cur.Status.Phase != v1alpha1.RunRunning || cur.Status.JobRef != nil {
			return nil
		}
		now := metav1.NewTime(r.now())
		cur.Status.JobRef = &v1alpha1.JobReference{Namespace: r.jobNamespace(), Name: jobName}
		cur.Status.Harness = harness
		cur.Status.StartedAt = &now
		cur.Status.ObservedGeneration = cur.Generation
		return r.Status().Update(ctx, &cur)
	})
}

// jobNamespace is recorded on JobRef for humans; the Runner already knows it.
func (r *UnitReconciler) jobNamespace() string { return "" }

// markWorkspaceLost surfaces the lost workspace on the parent as a Stalled
// condition, so the submission-level view carries the re-upload prompt too.
func (r *UnitReconciler) markWorkspaceLost(ctx context.Context, unit *v1alpha1.EvaluationUnit) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var eval v1alpha1.Evaluation
		if err := r.Get(ctx, types.NamespacedName{
			Namespace: unit.Namespace, Name: unit.Spec.EvaluationRef.Name,
		}, &eval); err != nil {
			return client.IgnoreNotFound(err)
		}
		meta.SetStatusCondition(&eval.Status.Conditions, metav1.Condition{
			Type:               v1alpha1.ConditionStalled,
			Status:             metav1.ConditionTrue,
			Reason:             string(v1alpha1.UnitWorkspaceLost),
			Message:            fmt.Sprintf("workspace %s is no longer cached", unit.Spec.Unit.Workspace.Digest),
			ObservedGeneration: eval.Generation,
		})
		return r.Status().Update(ctx, &eval)
	})
}

// collect reads a finished Job's event stream and applies the outcome.
func (r *UnitReconciler) collect(ctx context.Context, unit *v1alpha1.EvaluationUnit) (ctrl.Result, error) {
	st, err := r.Runner.Status(ctx, unit.Status.JobRef.Name)
	if kerrors.IsNotFound(err) {
		return ctrl.Result{}, r.settle(ctx, unit, settleOutcome{
			reason: v1alpha1.UnitAborted,
			detail: "agent job vanished before reporting",
		})
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	if !st.Done {
		return ctrl.Result{}, nil // the Job watch re-queues on completion
	}

	var result *evaluation.UnitResult
	var fatal string
	err = r.Runner.ResultLines(ctx, unit.Status.JobRef.Name, func(line []byte) error {
		ev, ok := evaluation.Decode(line)
		if !ok {
			return nil
		}
		switch ev.Type {
		case evaluation.TypeResult:
			result = ev.Result // keep the last
		case evaluation.TypeFatal:
			if fatal == "" {
				fatal = ev.Error // keep the first
			}
		}
		return nil
	})
	if err != nil {
		// The pods' logs may already be gone (TTL'd Job); treat as a failed
		// run rather than retrying forever.
		r.log().LogAttrs(ctx, slog.LevelWarn, "read evaluation job log",
			slog.String("unit", unit.Name), slog.Any("error", err))
	}

	switch {
	case result != nil:
		return ctrl.Result{}, r.apply(ctx, unit, result)
	case fatal != "":
		return ctrl.Result{}, r.settle(ctx, unit, settleOutcome{
			reason: v1alpha1.UnitJobFailed,
			detail: fatal,
		})
	default:
		return ctrl.Result{}, r.settle(ctx, unit, settleOutcome{
			reason: v1alpha1.UnitJobFailed,
			detail: "agent job finished without a result event",
		})
	}
}

// apply persists the entry and stamps the unit from the result summary, then
// rolls the parent up.
func (r *UnitReconciler) apply(ctx context.Context, unit *v1alpha1.EvaluationUnit,
	result *evaluation.UnitResult) error {
	oversize := len(result.Entry) > evaluation.MaxEntryBytes

	var resultsRef *v1alpha1.TranscriptRef
	if !oversize && len(result.Entry) > 0 {
		labels := map[string]string{
			v1alpha1.LabelEvaluation: unit.Spec.EvaluationRef.Name,
			v1alpha1.LabelUnitIndex:  fmt.Sprintf("%d", unit.Spec.Index),
		}
		ref, err := evalresults.Persist(ctx, r.Client, unit.Namespace, labels, unit, evuGVK, result.Entry)
		if err != nil {
			// Log-and-continue: the bounded summary still lands on status;
			// the client degrades to summary-only for this unit.
			r.log().LogAttrs(ctx, slog.LevelError, "persist evaluation results",
				slog.String("unit", unit.Name), slog.Any("error", err))
		} else {
			resultsRef = ref
		}
	}

	outcome := settleOutcome{result: result, resultsRef: resultsRef}
	if oversize {
		outcome.reason = v1alpha1.UnitResultTooLarge
		outcome.detail = fmt.Sprintf("result entry is %d bytes (cap %d)", len(result.Entry), evaluation.MaxEntryBytes)
	}
	return r.settle(ctx, unit, outcome)
}

// settleOutcome is one terminal unit state: a failure reason, or a result
// (or both, for ResultTooLarge — the summary survives, the entry does not).
type settleOutcome struct {
	reason     v1alpha1.UnitFailureReason
	detail     string
	result     *evaluation.UnitResult
	resultsRef *v1alpha1.TranscriptRef
}

// settle stamps a terminal unit status and rolls the parent up.
func (r *UnitReconciler) settle(ctx context.Context, unit *v1alpha1.EvaluationUnit, out settleOutcome) error {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var cur v1alpha1.EvaluationUnit
		if err := r.Get(ctx, client.ObjectKeyFromObject(unit), &cur); err != nil {
			return client.IgnoreNotFound(err)
		}
		if terminalRun(cur.Status.Phase) {
			return nil
		}
		now := metav1.NewTime(r.now())
		condStatus := metav1.ConditionTrue
		reason := "Complete"
		if out.reason != "" {
			cur.Status.Phase = v1alpha1.RunFailed
			cur.Status.Reason = out.reason
			cur.Status.Detail = agentresult.TruncateDetail(out.detail)
			condStatus = metav1.ConditionFalse
			reason = string(out.reason)
		} else {
			cur.Status.Phase = v1alpha1.RunComplete
		}
		if res := out.result; res != nil {
			stampSummary(&cur.Status, res)
			cur.Status.ResultsRef = out.resultsRef
		}
		cur.Status.FinishedAt = &now
		cur.Status.ObservedGeneration = cur.Generation
		meta.SetStatusCondition(&cur.Status.Conditions, metav1.Condition{
			Type:               v1alpha1.ConditionComplete,
			Status:             condStatus,
			Reason:             reason,
			Message:            cur.Status.Detail,
			ObservedGeneration: cur.Generation,
		})
		return r.Status().Update(ctx, &cur)
	})
	if err != nil {
		return err
	}
	return r.rollupParent(ctx, unit.Namespace, unit.Spec.EvaluationRef.Name)
}

// stampSummary copies the bounded result summary onto the unit status.
func stampSummary(st *v1alpha1.EvaluationUnitStatus, res *evaluation.UnitResult) {
	sum := res.Summary
	st.CasesPassed = int32(sum.CasesPassed)
	st.CasesFailed = int32(sum.CasesFailed)
	st.CasesErrored = int32(sum.CasesErrored)
	st.Cases = nil
	for _, c := range sum.Cases {
		if len(st.Cases) >= 256 {
			break
		}
		id := c.ID
		if len(id) > 128 {
			id = id[:128]
		}
		st.Cases = append(st.Cases, v1alpha1.CaseSummary{ID: id, Passed: c.Passed})
	}
	st.Usage = v1alpha1.UsageSummary{
		InputTokens:         sum.TokenUsage.InputTokens,
		OutputTokens:        sum.TokenUsage.OutputTokens,
		CacheReadTokens:     sum.TokenUsage.CacheReadTokens,
		CacheCreationTokens: sum.TokenUsage.CacheCreationTokens,
		CostUSD:             agentresult.FormatCost(sum.TokenUsage.CostUSD),
	}
	st.ElapsedMilliseconds = sum.ElapsedMS
	if st.Harness == "" {
		st.Harness = res.Harness
	}
}

// rollupParent recomputes the parent's counters and phase from a child List.
func (r *UnitReconciler) rollupParent(ctx context.Context, namespace, evalName string) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var eval v1alpha1.Evaluation
		if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: evalName}, &eval); err != nil {
			return client.IgnoreNotFound(err)
		}
		if terminalEvaluation(eval.Status.Phase) {
			return nil
		}
		var units v1alpha1.EvaluationUnitList
		if err := r.List(ctx, &units, client.InNamespace(namespace),
			client.MatchingLabels{v1alpha1.LabelEvaluation: evalName}); err != nil {
			return err
		}
		var complete, failed int32
		for i := range units.Items {
			switch units.Items[i].Status.Phase {
			case v1alpha1.RunComplete:
				complete++
			case v1alpha1.RunFailed:
				failed++
			}
		}
		eval.Status.UnitsComplete = complete
		eval.Status.UnitsFailed = failed
		total := int32(len(eval.Spec.Units))
		eval.Status.Units = total
		if complete+failed >= total && int32(len(units.Items)) >= total {
			now := metav1.NewTime(r.now())
			if failed > 0 {
				eval.Status.Phase = v1alpha1.EvaluationFailed
			} else {
				eval.Status.Phase = v1alpha1.EvaluationComplete
			}
			eval.Status.CompletedAt = &now
			meta.SetStatusCondition(&eval.Status.Conditions, metav1.Condition{
				Type:               v1alpha1.ConditionComplete,
				Status:             metav1.ConditionTrue,
				Reason:             string(eval.Status.Phase),
				Message:            fmt.Sprintf("%d/%d units complete, %d failed", complete, total, failed),
				ObservedGeneration: eval.Generation,
			})
		}
		return r.Status().Update(ctx, &eval)
	})
}

// terminalEvaluation reports whether an evaluation phase is settled.
func terminalEvaluation(p v1alpha1.EvaluationPhase) bool {
	return p == v1alpha1.EvaluationComplete || p == v1alpha1.EvaluationFailed
}

// finalize deletes the unit's Job (the Secret cascades) and releases the
// jobs finalizer.
func (r *UnitReconciler) finalize(ctx context.Context, unit *v1alpha1.EvaluationUnit) error {
	if err := r.Runner.Delete(ctx, jobs.EvalNameFor(unit.Name)); err != nil && !kerrors.IsNotFound(err) {
		return fmt.Errorf("delete job for %s: %w", unit.Name, err)
	}
	fins := unit.Finalizers
	out := fins[:0]
	for _, f := range fins {
		if f != v1alpha1.FinalizerJobs {
			out = append(out, f)
		}
	}
	if len(out) == len(fins) {
		return nil
	}
	unit.Finalizers = out
	if err := r.Update(ctx, unit); err != nil {
		return client.IgnoreNotFound(err)
	}
	return nil
}

func (r *UnitReconciler) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.New(slog.DiscardHandler)
}

// SetupWithManager registers the unit reconciler: every unit and evaluation
// Job event also fans into the singleton scheduler request.
func (r *UnitReconciler) SetupWithManager(mgr ctrl.Manager) error {
	mapJob := handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []ctrl.Request {
		if obj.GetLabels()[v1alpha1.LabelRunKind] != jobs.KindEvaluation {
			return nil
		}
		owner := obj.GetLabels()[v1alpha1.LabelOwner]
		if owner == "" {
			return nil
		}
		return []ctrl.Request{
			{NamespacedName: types.NamespacedName{Namespace: r.Namespace, Name: owner}},
			{NamespacedName: types.NamespacedName{Namespace: r.Namespace, Name: schedulerRequest}},
		}
	})
	mapSelf := handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []ctrl.Request {
		return []ctrl.Request{
			{NamespacedName: types.NamespacedName{Namespace: obj.GetNamespace(), Name: obj.GetName()}},
			{NamespacedName: types.NamespacedName{Namespace: obj.GetNamespace(), Name: schedulerRequest}},
		}
	})
	return ctrl.NewControllerManagedBy(mgr).
		Watches(&v1alpha1.EvaluationUnit{}, mapSelf).
		Watches(&batchv1.Job{}, mapJob).
		Named("evaluation-unit").
		Complete(r)
}
