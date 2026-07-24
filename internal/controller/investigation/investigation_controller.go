// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package investigation

import (
	"context"
	"fmt"
	"log/slog"
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

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/agentresult"
	"github.com/bitwise-media-group/patchy/internal/envelope"
	"github.com/bitwise-media-group/patchy/internal/jobs"
	"github.com/bitwise-media-group/patchy/internal/priority"
	"github.com/bitwise-media-group/patchy/internal/schedule"
	"github.com/bitwise-media-group/patchy/internal/templates"
)

// InvestigationPhaseIndex indexes Investigations by status.phase for the
// scheduler's slot accounting.
const InvestigationPhaseIndex = "status.phase"

// schedulerRequest is the singleton request every watch event maps to — one
// serialized decision point, MaxConcurrentReconciles=1 provides the mutex.
const schedulerRequest = "\x00scheduler"

// Runner is the slice of the jobs client this controller needs.
type Runner interface {
	Create(ctx context.Context, spec jobs.Spec) (string, error)
	Result(ctx context.Context, jobName string) ([]envelope.Event, error)
	Status(ctx context.Context, jobName string) (jobs.Status, error)
	Delete(ctx context.Context, jobName string) error
}

// InvestigationReconciler grants bounded slots to pending investigations
// (severity-priority order), launches the agent Job, and applies the result
// when the Job completes.
type InvestigationReconciler struct {
	client.Client
	// Runner creates and observes agent Jobs (in the agents namespace).
	Runner Runner
	// Namespace the CRs live in.
	Namespace string
	// MaxConcurrent bounds simultaneously running investigations.
	MaxConcurrent int
	// MaxAttempts bounds analysis attempts per finding before Failed.
	MaxAttempts int32
	// ConfidenceThreshold gates automated remediation queueing.
	ConfidenceThreshold float64
	// Aging lifts long-waiting investigations.
	Aging schedule.AgingPolicy
	// InvestigateHarness/InvestigateModel are the harness id and canonical
	// model the analysis stage runs on, resolved at startup; the Job carries
	// them so its pod runs the harness its runner image was built for.
	InvestigateHarness string
	InvestigateModel   string
	// Now is the clock seam; nil means time.Now.
	Now func() time.Time
	// Log receives diagnostics; nil discards.
	Log *slog.Logger
}

// Reconcile handles both the singleton scheduling request and per-object
// runs.
func (r *InvestigationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if req.Name == schedulerRequest {
		return r.schedule(ctx)
	}
	return r.run(ctx, req)
}

// schedule grants free slots to the highest-priority pending
// investigations. Slot accounting comes from the cluster, never memory.
func (r *InvestigationReconciler) schedule(ctx context.Context) (ctrl.Result, error) {
	var list v1alpha1.InvestigationList
	if err := r.List(ctx, &list, client.InNamespace(r.Namespace)); err != nil {
		return ctrl.Result{}, err
	}
	running := 0
	var pending []schedule.Candidate
	for i := range list.Items {
		inv := &list.Items[i]
		switch inv.Status.Phase {
		case v1alpha1.RunRunning:
			running++
		case v1alpha1.RunPending, "":
			if !inv.DeletionTimestamp.IsZero() {
				continue
			}
			sev := v1alpha1.Level(inv.Labels[v1alpha1.LabelSeverity])
			pending = append(pending, schedule.Candidate{
				Name:      inv.Name,
				Priority:  priority.Score(sev, "", "", "", priority.DefaultWeights),
				QueuedAt:  inv.CreationTimestamp.Time,
				Expedited: r.expedited(ctx, inv.Namespace, inv.Spec.FindingRef.Name),
			})
		}
	}
	maxConcurrent := r.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = 3
	}
	for _, name := range schedule.Pick(pending, maxConcurrent-running, r.now(), r.Aging) {
		if err := r.grant(ctx, name); err != nil {
			return ctrl.Result{}, err
		}
	}
	// Safety tick: re-inspect even if an event is lost.
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

// expedited reads the parent finding's expedite mark (cached client; a miss
// simply ranks the run normally).
func (r *InvestigationReconciler) expedited(ctx context.Context, namespace, finding string) bool {
	if finding == "" {
		return false
	}
	var fnd v1alpha1.Finding
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: finding}, &fnd); err != nil {
		return false
	}
	return fnd.Spec.Expedite != nil
}

// grant moves one investigation to Running (idempotent).
func (r *InvestigationReconciler) grant(ctx context.Context, name string) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var inv v1alpha1.Investigation
		if err := r.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: name}, &inv); err != nil {
			return client.IgnoreNotFound(err)
		}
		if inv.Status.Phase != "" && inv.Status.Phase != v1alpha1.RunPending {
			return nil
		}
		inv.Status.Phase = v1alpha1.RunRunning
		return r.Status().Update(ctx, &inv)
	})
}

// run drives one investigation: launch its Job when granted, apply its
// result when the Job finishes, clean up on deletion.
func (r *InvestigationReconciler) run(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var inv v1alpha1.Investigation
	if err := r.Get(ctx, req.NamespacedName, &inv); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !inv.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.finalize(ctx, &inv)
	}

	switch inv.Status.Phase {
	case v1alpha1.RunRunning:
		if inv.Status.JobRef == nil {
			return ctrl.Result{}, r.launch(ctx, &inv)
		}
		return r.collect(ctx, &inv)
	default:
		return ctrl.Result{}, nil // Pending waits for a grant; terminal is done
	}
}

// launch creates the agent Job for a granted investigation.
func (r *InvestigationReconciler) launch(ctx context.Context, inv *v1alpha1.Investigation) error {
	var fnd v1alpha1.Finding
	fndKey := types.NamespacedName{Namespace: inv.Namespace, Name: inv.Spec.FindingRef.Name}
	if err := r.Get(ctx, fndKey, &fnd); err != nil {
		return client.IgnoreNotFound(err)
	}
	var repo v1alpha1.Repository
	if inv.Spec.RepositoryRef == nil {
		return r.fail(ctx, inv, &fnd, "aborted", "investigation has no repository artifact")
	}
	repoKey := types.NamespacedName{Namespace: inv.Namespace, Name: inv.Spec.RepositoryRef.Name}
	if err := r.Get(ctx, repoKey, &repo); err != nil {
		return err
	}
	if repo.Status.Artifact == nil {
		return fmt.Errorf("repository %s has no artifact yet", repo.Name)
	}
	handoff, err := templates.RenderFindingIssue(&fnd)
	if err != nil {
		return err
	}

	repoName := ""
	if fnd.Spec.Repository != nil {
		repoName = fnd.Spec.Repository.Name
	}
	jobName, err := r.Runner.Create(ctx, jobs.Spec{
		Repo:           repoName,
		Attempt:        int(inv.Spec.Attempt),
		Phase:          "investigate",
		Harness:        r.InvestigateHarness,
		Model:          r.InvestigateModel,
		BaseSHA:        repo.Status.ResolvedSHA,
		IssueMarkdown:  handoff,
		Kind:           string(v1alpha1.RunKindInvestigation),
		Owner:          inv.Name,
		Finding:        fnd.Name,
		ArtifactURL:    repo.Status.Artifact.URL,
		ArtifactDigest: repo.Status.Artifact.Digest,
	})
	if err != nil {
		return fmt.Errorf("launch investigation job: %w", err)
	}

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var cur v1alpha1.Investigation
		if err := r.Get(ctx, client.ObjectKeyFromObject(inv), &cur); err != nil {
			return client.IgnoreNotFound(err)
		}
		cur.Status.JobRef = &v1alpha1.JobReference{Namespace: jobNamespace(jobName), Name: jobName}
		return r.Status().Update(ctx, &cur)
	})
}

// jobNamespace is stamped for observability; the jobs client owns the real
// namespace, so this is cosmetic here (empty means "the agents namespace").
func jobNamespace(string) string { return "" }

// collect waits for the Job to finish and applies its result.
func (r *InvestigationReconciler) collect(ctx context.Context, inv *v1alpha1.Investigation) (ctrl.Result, error) {
	st, err := r.Runner.Status(ctx, inv.Status.JobRef.Name)
	if err != nil {
		if kerrors.IsNotFound(err) {
			// The Job vanished (TTL, manual delete) without a result.
			var fnd v1alpha1.Finding
			key := types.NamespacedName{Namespace: inv.Namespace, Name: inv.Spec.FindingRef.Name}
			if err := r.Get(ctx, key, &fnd); err != nil {
				return ctrl.Result{}, client.IgnoreNotFound(err)
			}
			return ctrl.Result{}, r.fail(ctx, inv, &fnd, "aborted", "agent job vanished before reporting")
		}
		return ctrl.Result{}, err
	}
	if !st.Done {
		return ctrl.Result{}, nil // the Job watch re-queues us on completion
	}

	events, err := r.Runner.Result(ctx, inv.Status.JobRef.Name)
	if err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, r.apply(ctx, inv, events)
}

// apply routes the Job's investigation event onto the child and the Finding.
func (r *InvestigationReconciler) apply(
	ctx context.Context, inv *v1alpha1.Investigation, events []envelope.Event,
) error {
	var fnd v1alpha1.Finding
	key := types.NamespacedName{Namespace: inv.Namespace, Name: inv.Spec.FindingRef.Name}
	if err := r.Get(ctx, key, &fnd); err != nil {
		return client.IgnoreNotFound(err)
	}

	var result *envelope.Investigation
	for _, e := range events {
		switch e.Type {
		case envelope.TypeInvestigation:
			result = e.Investigation
		case envelope.TypeFatal:
			return r.fail(ctx, inv, &fnd, "aborted", e.Error)
		}
	}
	if result == nil {
		return r.fail(ctx, inv, &fnd, "aborted", "agent job produced no investigation event")
	}
	if result.Outcome != envelope.OutcomeOK {
		return r.fail(ctx, inv, &fnd, string(result.Outcome), result.Detail)
	}

	// Stamp the child (single writer: this controller).
	if err := r.stampChild(ctx, inv, result, v1alpha1.RunComplete); err != nil {
		return err
	}

	// Route the finding.
	to, priorityLevel := r.route(&fnd, result)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var cur v1alpha1.Finding
		if err := r.Get(ctx, key, &cur); err != nil {
			return client.IgnoreNotFound(err)
		}
		summary := &v1alpha1.InvestigationSummary{
			Name:           inv.Name,
			Attempt:        inv.Spec.Attempt,
			Outcome:        string(result.Outcome),
			Recommendation: v1alpha1.Recommendation(result.Recommendation),
			Confidence:     agentresult.FormatConfidence(result.Confidence),
			Exploitability: v1alpha1.Rating(result.Exploitability.Rating),
			Likelihood:     v1alpha1.Rating(result.Likelihood.Rating),
			Impact:         v1alpha1.Rating(result.Impact.Rating),
			AwaitApproval:  result.AwaitApproval,
			CompletedAt:    timePtr(r.now()),
		}
		cur.Status.Investigation = summary
		cur.Status.Priority = priorityLevel
		cur.Status.ActiveRun = nil
		meta.SetStatusCondition(&cur.Status.Conditions, metav1.Condition{
			Type:               v1alpha1.ConditionInvestigated,
			Status:             metav1.ConditionTrue,
			Reason:             nonEmpty(result.Recommendation, "Unknown"),
			ObservedGeneration: cur.Generation,
		})
		if cur.Status.Phase == v1alpha1.PhaseInvestigating {
			if err := v1alpha1.SetPhase(&cur, to, r.now()); err != nil {
				return err
			}
		}
		return r.Status().Update(ctx, &cur)
	})
}

// route maps a completed analysis onto the finding's next phase (edges 5–8)
// and the display priority level.
func (r *InvestigationReconciler) route(
	fnd *v1alpha1.Finding, result *envelope.Investigation,
) (v1alpha1.Phase, v1alpha1.Level) {
	level := v1alpha1.Level(result.Priority)
	switch v1alpha1.Recommendation(result.Recommendation) {
	case v1alpha1.RecommendationIgnore:
		return v1alpha1.PhaseDismissed, level
	case v1alpha1.RecommendationManual:
		return v1alpha1.PhaseHandedOff, level
	case v1alpha1.RecommendationRemediate:
		if fnd.Spec.Repository == nil {
			return v1alpha1.PhaseHandedOff, level
		}
		if result.AwaitApproval || result.Confidence < r.ConfidenceThreshold {
			return v1alpha1.PhaseAwaitingApproval, level
		}
		return v1alpha1.PhaseQueued, level
	default:
		return v1alpha1.PhaseHandedOff, level
	}
}

// fail stamps a failed run and either reverts the finding for a retry or
// exhausts it (edges 4 and 9).
func (r *InvestigationReconciler) fail(
	ctx context.Context, inv *v1alpha1.Investigation, fnd *v1alpha1.Finding, outcome, detail string,
) error {
	result := &envelope.Investigation{Stage: envelope.Stage{Outcome: envelope.Outcome(outcome), Detail: detail}}
	if err := r.stampChild(ctx, inv, result, v1alpha1.RunFailed); err != nil {
		return err
	}
	maxAttempts := r.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 2
	}
	to := v1alpha1.PhaseEnhanced
	if inv.Spec.Attempt >= maxAttempts {
		to = v1alpha1.PhaseFailed
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var cur v1alpha1.Finding
		if err := r.Get(ctx, client.ObjectKeyFromObject(fnd), &cur); err != nil {
			return client.IgnoreNotFound(err)
		}
		if cur.Status.Phase != v1alpha1.PhaseInvestigating {
			return nil
		}
		if err := v1alpha1.SetPhase(&cur, to, r.now()); err != nil {
			return err
		}
		cur.Status.ActiveRun = nil
		cur.Status.LastFailureReason = agentresult.TruncateDetail(outcome + ": " + detail)
		return r.Status().Update(ctx, &cur)
	})
}

// stampChild writes the run result onto the Investigation.
func (r *InvestigationReconciler) stampChild(
	ctx context.Context, inv *v1alpha1.Investigation, result *envelope.Investigation, phase v1alpha1.RunPhase,
) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var cur v1alpha1.Investigation
		if err := r.Get(ctx, client.ObjectKeyFromObject(inv), &cur); err != nil {
			return client.IgnoreNotFound(err)
		}
		cur.Status.Phase = phase
		cur.Status.Stage = agentresult.FromStage(&result.Stage)
		cur.Status.Report = agentresult.TruncateReport(result.ReportMarkdown)
		if phase == v1alpha1.RunComplete {
			cur.Status.Exploitability = agentresult.Analysis(result.Exploitability)
			cur.Status.Likelihood = agentresult.Analysis(result.Likelihood)
			cur.Status.Impact = agentresult.Analysis(result.Impact)
			cur.Status.Recommendation = v1alpha1.Recommendation(result.Recommendation)
			cur.Status.Confidence = agentresult.FormatConfidence(result.Confidence)
			cur.Status.Severity = v1alpha1.Level(result.Severity)
			cur.Status.Priority = v1alpha1.Level(result.Priority)
			cur.Status.AwaitApproval = result.AwaitApproval
			if result.RemediationModel != "" || result.MaxTurns > 0 || result.TokenBudget > 0 {
				cur.Status.RemediationParameters = &v1alpha1.AgentParameters{
					Model:       result.RemediationModel,
					MaxTurns:    int32(result.MaxTurns),
					TokenBudget: int64(result.TokenBudget),
				}
			}
		}
		meta.SetStatusCondition(&cur.Status.Conditions, metav1.Condition{
			Type:               v1alpha1.ConditionComplete,
			Status:             metav1.ConditionTrue,
			Reason:             nonEmpty(string(result.Outcome), "Unknown"),
			Message:            result.Detail,
			ObservedGeneration: cur.Generation,
		})
		return r.Status().Update(ctx, &cur)
	})
}

// finalize cleans the child's Job/Secret before letting deletion proceed
// (the FinalizerJobs contract).
func (r *InvestigationReconciler) finalize(ctx context.Context, inv *v1alpha1.Investigation) error {
	if inv.Status.JobRef != nil {
		if err := r.Runner.Delete(ctx, inv.Status.JobRef.Name); err != nil && !kerrors.IsNotFound(err) {
			return err
		}
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var cur v1alpha1.Investigation
		if err := r.Get(ctx, client.ObjectKeyFromObject(inv), &cur); err != nil {
			return client.IgnoreNotFound(err)
		}
		fins := cur.GetFinalizers()
		out := fins[:0]
		for _, f := range fins {
			if f != v1alpha1.FinalizerJobs {
				out = append(out, f)
			}
		}
		if len(out) == len(fins) {
			return nil
		}
		cur.SetFinalizers(out)
		return r.Update(ctx, &cur)
	})
}

// SetupWithManager wires the reconciler: Investigations, plus agent-Job
// completions mapped back (by owner label, filtered to this kind), all also
// fanned into the singleton scheduling request.
func (r *InvestigationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	mapJob := handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []ctrl.Request {
		if obj.GetLabels()[v1alpha1.LabelRunKind] != string(v1alpha1.RunKindInvestigation) {
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
		Watches(&v1alpha1.Investigation{}, mapSelf).
		Watches(&batchv1.Job{}, mapJob).
		Named("investigation").
		Complete(r)
}

func timePtr(t time.Time) *metav1.Time {
	mt := metav1.NewTime(t)
	return &mt
}

func nonEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func (r *InvestigationReconciler) now() time.Time {
	if r.Now == nil {
		return time.Now()
	}
	return r.Now()
}
