// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package remediation

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
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
	"github.com/bitwise-media-group/patchy/internal/report"
	"github.com/bitwise-media-group/patchy/internal/schedule"
	"github.com/bitwise-media-group/patchy/internal/templates"
)

// remSchedulerRequest is the singleton request every watch event maps to.
const remSchedulerRequest = "\x00scheduler"

// CRRunner is the slice of the jobs client the CRD-native remediation
// engine needs (distinct name: the legacy Runner interface lives in
// controller.go until the cutover).
type CRRunner interface {
	Create(ctx context.Context, spec jobs.Spec) (string, error)
	Result(ctx context.Context, jobName string) ([]envelope.Event, error)
	Status(ctx context.Context, jobName string) (jobs.Status, error)
	Delete(ctx context.Context, jobName string) error
}

// ForgeWriter performs the two forge writes remediation owns: the branch
// push (Git Data API, scoped write token) and the pull request. It wraps
// internal/forge + internal/ghpush; tests substitute a fake.
type ForgeWriter interface {
	// Push replays the changeset as branch on the repository.
	Push(ctx context.Context, namespace, repoURL, branch string, cs *envelope.Changeset) error
	// EnsurePR opens (or finds, idempotently by head branch) the pull
	// request for branch.
	EnsurePR(ctx context.Context, namespace, repoURL, branch, title, body string) (number int64, url string, err error)
}

// RemediationReconciler grants bounded slots to pending remediations in
// priority order, launches the agent Job, and applies results: push + PR on
// success, retry or exhaustion on failure.
type RemediationReconciler struct {
	client.Client
	// Runner creates and observes agent Jobs.
	Runner CRRunner
	// Forge performs the branch push and PR creation.
	Forge ForgeWriter
	// Namespace the CRs live in.
	Namespace string
	// MaxConcurrent bounds simultaneously running remediations (default 1).
	MaxConcurrent int
	// MaxAttempts bounds remediation attempts per finding.
	MaxAttempts int32
	// Aging lifts long-waiting remediations.
	Aging schedule.AgingPolicy
	// Enabled is the set of harness ids this deployment can run. launch
	// re-checks the spawner-resolved harness against it, so an enabled-set
	// change between spawn and launch fails the run cleanly instead of
	// creating an unrunnable Job.
	Enabled []string
	// Now is the clock seam; nil means time.Now.
	Now func() time.Time
	// Log receives diagnostics; nil discards.
	Log *slog.Logger
}

// Reconcile handles the singleton scheduler request and per-object runs.
func (r *RemediationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if req.Name == remSchedulerRequest {
		return r.schedule(ctx)
	}
	return r.run(ctx, req)
}

// schedule grants free slots to the highest-priority pending remediations.
func (r *RemediationReconciler) schedule(ctx context.Context) (ctrl.Result, error) {
	var list v1alpha1.RemediationList
	if err := r.List(ctx, &list, client.InNamespace(r.Namespace)); err != nil {
		return ctrl.Result{}, err
	}
	running := 0
	var pending []schedule.Candidate
	for i := range list.Items {
		rem := &list.Items[i]
		switch rem.Status.Phase {
		case v1alpha1.RunRunning:
			running++
		case v1alpha1.RunPending, "":
			if !rem.DeletionTimestamp.IsZero() {
				continue
			}
			pending = append(pending, schedule.Candidate{
				Name:      rem.Name,
				Priority:  rem.Spec.Priority,
				QueuedAt:  rem.CreationTimestamp.Time,
				Expedited: r.expedited(ctx, rem.Namespace, rem.Spec.FindingRef.Name),
			})
		}
	}
	maxConcurrent := r.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	for _, name := range schedule.Pick(pending, maxConcurrent-running, r.now(), r.Aging) {
		if err := r.grant(ctx, name); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

// expedited reads the parent finding's expedite mark (cached client; a miss
// simply ranks the run normally).
func (r *RemediationReconciler) expedited(ctx context.Context, namespace, finding string) bool {
	if finding == "" {
		return false
	}
	var fnd v1alpha1.Finding
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: finding}, &fnd); err != nil {
		return false
	}
	return fnd.Spec.Expedite != nil
}

// grant moves one remediation to Running and its finding to Remediating
// (edge 11).
func (r *RemediationReconciler) grant(ctx context.Context, name string) error {
	var findingName string
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var rem v1alpha1.Remediation
		if err := r.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: name}, &rem); err != nil {
			return client.IgnoreNotFound(err)
		}
		if rem.Status.Phase != "" && rem.Status.Phase != v1alpha1.RunPending {
			return nil
		}
		now := metav1.NewTime(r.now())
		rem.Status.Phase = v1alpha1.RunRunning
		rem.Status.GrantedAt = &now
		findingName = rem.Spec.FindingRef.Name
		return r.Status().Update(ctx, &rem)
	})
	if err != nil || findingName == "" {
		return err
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var fnd v1alpha1.Finding
		if err := r.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: findingName}, &fnd); err != nil {
			return client.IgnoreNotFound(err)
		}
		if fnd.Status.Phase != v1alpha1.PhaseQueued {
			return nil
		}
		if err := v1alpha1.SetPhase(&fnd, v1alpha1.PhaseRemediating, r.now()); err != nil {
			return err
		}
		fnd.Status.ActiveRun = &v1alpha1.ActiveRun{Kind: v1alpha1.RunKindRemediation, Name: name}
		return r.Status().Update(ctx, &fnd)
	})
}

// run drives one remediation object.
func (r *RemediationReconciler) run(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var rem v1alpha1.Remediation
	if err := r.Get(ctx, req.NamespacedName, &rem); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !rem.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.finalize(ctx, &rem)
	}
	switch rem.Status.Phase {
	case v1alpha1.RunRunning:
		if rem.Status.JobRef == nil {
			return ctrl.Result{}, r.launch(ctx, &rem)
		}
		return r.collect(ctx, &rem)
	default:
		return ctrl.Result{}, nil
	}
}

// launch creates the remediation agent Job (credential-less artifact flow;
// the investigation report rides along as the analysis input).
func (r *RemediationReconciler) launch(ctx context.Context, rem *v1alpha1.Remediation) error {
	var fnd v1alpha1.Finding
	fndKey := types.NamespacedName{Namespace: rem.Namespace, Name: rem.Spec.FindingRef.Name}
	if err := r.Get(ctx, fndKey, &fnd); err != nil {
		return client.IgnoreNotFound(err)
	}
	var repo v1alpha1.Repository
	repoKey := types.NamespacedName{Namespace: rem.Namespace, Name: rem.Spec.RepositoryRef.Name}
	if err := r.Get(ctx, repoKey, &repo); err != nil {
		return err
	}
	if repo.Status.Artifact == nil {
		return fmt.Errorf("repository %s has no artifact", repo.Name)
	}
	var inv v1alpha1.Investigation
	invKey := types.NamespacedName{Namespace: rem.Namespace, Name: rem.Spec.InvestigationRef.Name}
	if err := r.Get(ctx, invKey, &inv); err != nil {
		return err
	}
	handoff, err := templates.RenderFindingIssue(&fnd)
	if err != nil {
		return err
	}

	// The spawner resolved the model to a harness (and thus a runner image and
	// credential). Re-check it is still enabled; an enabled-set change between
	// spawn and launch fails the run cleanly rather than creating a Job for a
	// runner that no longer exists.
	harnessID := rem.Spec.Parameters.Harness
	if harnessID == "" || !slices.Contains(r.Enabled, harnessID) {
		return r.fail(ctx, rem, "aborted",
			fmt.Sprintf("harness %q for model %q is not enabled", harnessID, rem.Spec.Parameters.Model))
	}

	repoName := ""
	if fnd.Spec.Repository != nil {
		repoName = fnd.Spec.Repository.Name
	}
	jobName, err := r.Runner.Create(ctx, jobs.Spec{
		Repo:                  repoName,
		Attempt:               int(rem.Spec.Attempt),
		Phase:                 "remediate",
		Harness:               harnessID,
		Model:                 rem.Spec.Parameters.Model,
		BaseSHA:               repo.Status.ResolvedSHA,
		IssueMarkdown:         handoff,
		InvestigationMarkdown: inv.Status.Report,
		Kind:                  string(v1alpha1.RunKindRemediation),
		Owner:                 rem.Name,
		Finding:               fnd.Name,
		ArtifactURL:           repo.Status.Artifact.URL,
		ArtifactDigest:        repo.Status.Artifact.Digest,
	})
	if err != nil {
		return fmt.Errorf("launch remediation job: %w", err)
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var cur v1alpha1.Remediation
		if err := r.Get(ctx, client.ObjectKeyFromObject(rem), &cur); err != nil {
			return client.IgnoreNotFound(err)
		}
		cur.Status.JobRef = &v1alpha1.JobReference{Name: jobName}
		return r.Status().Update(ctx, &cur)
	})
}

// collect applies the result once the Job finishes.
func (r *RemediationReconciler) collect(ctx context.Context, rem *v1alpha1.Remediation) (ctrl.Result, error) {
	st, err := r.Runner.Status(ctx, rem.Status.JobRef.Name)
	if err != nil {
		if kerrors.IsNotFound(err) {
			return ctrl.Result{}, r.fail(ctx, rem, "aborted", "agent job vanished before reporting")
		}
		return ctrl.Result{}, err
	}
	if !st.Done {
		return ctrl.Result{}, nil
	}
	events, err := r.Runner.Result(ctx, rem.Status.JobRef.Name)
	if err != nil {
		return ctrl.Result{}, err
	}

	var result *envelope.Remediation
	for _, e := range events {
		switch e.Type {
		case envelope.TypeRemediation:
			result = e.Remediation
		case envelope.TypeFatal:
			return ctrl.Result{}, r.fail(ctx, rem, "aborted", e.Error)
		}
	}
	switch {
	case result == nil:
		return ctrl.Result{}, r.fail(ctx, rem, "aborted", "agent job produced no remediation event")
	case result.Outcome != envelope.OutcomeOK:
		return ctrl.Result{}, r.fail(ctx, rem, string(result.Outcome), result.Detail)
	case !result.Success:
		return ctrl.Result{}, r.handOff(ctx, rem, result)
	default:
		return ctrl.Result{}, r.succeed(ctx, rem, result)
	}
}

// succeed pushes the changeset, opens the PR, and moves the finding to
// InReview (edge 13). The changeset never touches any CR.
func (r *RemediationReconciler) succeed(
	ctx context.Context, rem *v1alpha1.Remediation, result *envelope.Remediation,
) error {
	var fnd v1alpha1.Finding
	fndKey := types.NamespacedName{Namespace: rem.Namespace, Name: rem.Spec.FindingRef.Name}
	if err := r.Get(ctx, fndKey, &fnd); err != nil {
		return client.IgnoreNotFound(err)
	}
	if fnd.Spec.Repository == nil || result.Changeset == nil {
		return r.fail(ctx, rem, "aborted", "success without changeset or repository")
	}
	branch := "patchy/" + fnd.Name
	if err := r.Forge.Push(ctx, rem.Namespace, fnd.Spec.Repository.URL, branch, result.Changeset); err != nil {
		return fmt.Errorf("push remediation branch: %w", err)
	}

	issueNumber := 0
	if fnd.Status.Tracking != nil {
		issueNumber = int(fnd.Status.Tracking.IssueNumber)
	}
	// The stored report carries its machine frontmatter; the PR body is
	// presentation, so render the markdown body only.
	reportBody := report.StripFrontmatter(result.ReportMarkdown)
	body, err := templates.PRBody(issueNumber, reportBody)
	if err != nil {
		body = reportBody
	}
	number, url, err := r.Forge.EnsurePR(ctx, rem.Namespace, fnd.Spec.Repository.URL, branch,
		templates.FindingIssueTitle(&fnd), body)
	if err != nil {
		return fmt.Errorf("open remediation PR: %w", err)
	}

	if err := r.stampChild(ctx, rem, result, v1alpha1.RunComplete, func(cur *v1alpha1.Remediation) {
		cur.Status.Success = true
		cur.Status.Branch = branch
		cur.Status.Confidence = agentresult.FormatConfidence(result.Confidence)
		cur.Status.PullRequest = &v1alpha1.PullRequestRef{Number: number, URL: url}
	}); err != nil {
		return err
	}

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var cur v1alpha1.Finding
		if err := r.Get(ctx, fndKey, &cur); err != nil {
			return client.IgnoreNotFound(err)
		}
		cur.Status.Remediation = &v1alpha1.RemediationSummary{
			Name: rem.Name, Attempt: rem.Spec.Attempt,
			Outcome: string(result.Outcome), Success: true, Branch: branch,
			CompletedAt: timePtr(r.now()),
		}
		cur.Status.PullRequest = &v1alpha1.PullRequestStatus{Number: number, URL: url, State: "open"}
		cur.Status.ActiveRun = nil
		if cur.Status.Phase == v1alpha1.PhaseRemediating {
			if err := v1alpha1.SetPhase(&cur, v1alpha1.PhaseInReview, r.now()); err != nil {
				return err
			}
		}
		return r.Status().Update(ctx, &cur)
	})
}

// handOff records an agent's "not safely fixable" verdict (edge 14).
func (r *RemediationReconciler) handOff(
	ctx context.Context, rem *v1alpha1.Remediation, result *envelope.Remediation,
) error {
	if err := r.stampChild(ctx, rem, result, v1alpha1.RunComplete, func(cur *v1alpha1.Remediation) {
		cur.Status.Success = false
		cur.Status.Confidence = agentresult.FormatConfidence(result.Confidence)
	}); err != nil {
		return err
	}
	return r.finishFinding(ctx, rem, result, v1alpha1.PhaseHandedOff, "agent reported the finding is not safely fixable")
}

// fail retries (edge 12) or exhausts (edge 15).
func (r *RemediationReconciler) fail(ctx context.Context, rem *v1alpha1.Remediation, outcome, detail string) error {
	result := &envelope.Remediation{Stage: envelope.Stage{Outcome: envelope.Outcome(outcome), Detail: detail}}
	if err := r.stampChild(ctx, rem, result, v1alpha1.RunFailed, nil); err != nil {
		return err
	}
	maxAttempts := r.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 2
	}
	to := v1alpha1.PhaseQueued
	if rem.Spec.Attempt >= maxAttempts {
		to = v1alpha1.PhaseFailed
	}
	return r.finishFinding(ctx, rem, result, to, outcome+": "+detail)
}

// finishFinding applies the post-run phase and summary to the finding.
func (r *RemediationReconciler) finishFinding(
	ctx context.Context, rem *v1alpha1.Remediation, result *envelope.Remediation,
	to v1alpha1.Phase, reason string,
) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var cur v1alpha1.Finding
		key := types.NamespacedName{Namespace: rem.Namespace, Name: rem.Spec.FindingRef.Name}
		if err := r.Get(ctx, key, &cur); err != nil {
			return client.IgnoreNotFound(err)
		}
		if cur.Status.Phase != v1alpha1.PhaseRemediating {
			return nil
		}
		cur.Status.Remediation = &v1alpha1.RemediationSummary{
			Name: rem.Name, Attempt: rem.Spec.Attempt,
			Outcome: string(result.Outcome), Success: false,
			CompletedAt: timePtr(r.now()),
		}
		cur.Status.ActiveRun = nil
		cur.Status.LastFailureReason = agentresult.TruncateDetail(reason)
		if err := v1alpha1.SetPhase(&cur, to, r.now()); err != nil {
			return err
		}
		return r.Status().Update(ctx, &cur)
	})
}

// stampChild writes the run result onto the Remediation.
func (r *RemediationReconciler) stampChild(
	ctx context.Context, rem *v1alpha1.Remediation, result *envelope.Remediation,
	phase v1alpha1.RunPhase, extra func(*v1alpha1.Remediation),
) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var cur v1alpha1.Remediation
		if err := r.Get(ctx, client.ObjectKeyFromObject(rem), &cur); err != nil {
			return client.IgnoreNotFound(err)
		}
		cur.Status.Phase = phase
		cur.Status.Stage = agentresult.FromStage(&result.Stage)
		cur.Status.Report = agentresult.TruncateReport(result.ReportMarkdown)
		if extra != nil {
			extra(&cur)
		}
		meta.SetStatusCondition(&cur.Status.Conditions, metav1.Condition{
			Type:               v1alpha1.ConditionComplete,
			Status:             metav1.ConditionTrue,
			Reason:             nonEmptyReason(string(result.Outcome)),
			Message:            result.Detail,
			ObservedGeneration: cur.Generation,
		})
		return r.Status().Update(ctx, &cur)
	})
}

// finalize cleans the child's Job/Secret before deletion (FinalizerJobs).
func (r *RemediationReconciler) finalize(ctx context.Context, rem *v1alpha1.Remediation) error {
	if rem.Status.JobRef != nil {
		if err := r.Runner.Delete(ctx, rem.Status.JobRef.Name); err != nil && !kerrors.IsNotFound(err) {
			return err
		}
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var cur v1alpha1.Remediation
		if err := r.Get(ctx, client.ObjectKeyFromObject(rem), &cur); err != nil {
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

// SetupWithManager wires the reconciler and its scheduler fan-in.
func (r *RemediationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	mapJob := handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []ctrl.Request {
		if obj.GetLabels()[v1alpha1.LabelRunKind] != string(v1alpha1.RunKindRemediation) {
			return nil
		}
		owner := obj.GetLabels()[v1alpha1.LabelOwner]
		if owner == "" {
			return nil
		}
		return []ctrl.Request{
			{NamespacedName: types.NamespacedName{Namespace: r.Namespace, Name: owner}},
			{NamespacedName: types.NamespacedName{Namespace: r.Namespace, Name: remSchedulerRequest}},
		}
	})
	mapSelf := handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []ctrl.Request {
		return []ctrl.Request{
			{NamespacedName: types.NamespacedName{Namespace: obj.GetNamespace(), Name: obj.GetName()}},
			{NamespacedName: types.NamespacedName{Namespace: obj.GetNamespace(), Name: remSchedulerRequest}},
		}
	})
	return ctrl.NewControllerManagedBy(mgr).
		Watches(&v1alpha1.Remediation{}, mapSelf).
		Watches(&batchv1.Job{}, mapJob).
		Named("remediation-run").
		Complete(r)
}

// nonEmptyReason keeps condition reasons non-empty.
func nonEmptyReason(s string) string {
	if s == "" {
		return "Unknown"
	}
	return s
}

// timePtr wraps a time for CRD fields.
func timePtr(t time.Time) *metav1.Time {
	mt := metav1.NewTime(t)
	return &mt
}

func (r *RemediationReconciler) now() time.Time {
	if r.Now == nil {
		return time.Now()
	}
	return r.Now()
}
