// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package integration

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/ghclient"
	"github.com/bitwise-media-group/patchy/internal/labels"
	"github.com/bitwise-media-group/patchy/internal/templates"
)

// Projection-state annotations on the Finding: what has already been pushed
// to the tracking item, so re-reconciles stay idempotent without re-reading
// comments. All are integration-controller-owned.
const (
	// AnnotationProjectedBody is the hash of the last rendered body+labels.
	AnnotationProjectedBody = "patchy.bitwisemedia.uk/projected-body"
	// AnnotationProjectedEnrichments counts enrichment comments posted.
	AnnotationProjectedEnrichments = "patchy.bitwisemedia.uk/projected-enrichments"
	// AnnotationProjectedInvestigation names the Investigation whose report
	// comment was posted.
	AnnotationProjectedInvestigation = "patchy.bitwisemedia.uk/projected-investigation"
	// AnnotationProjectedRemediation names the Remediation whose report
	// comment was posted.
	AnnotationProjectedRemediation = "patchy.bitwisemedia.uk/projected-remediation"
	// AnnotationProjectedNotice records the phase whose human notice
	// (hold/hand-off/failure) was posted.
	AnnotationProjectedNotice = "patchy.bitwisemedia.uk/projected-notice"
)

// trackerClient is the tracking-system surface the projection needs — a
// slice of ghclient.Client; tests substitute a fake.
type trackerClient interface {
	Create(ctx context.Context, repo ghclient.Repo, req ghclient.IssueRequest) (*ghclient.Issue, error)
	GetIssue(ctx context.Context, repo ghclient.Repo, number int) (*ghclient.Issue, error)
	EditBody(ctx context.Context, repo ghclient.Repo, number int, body string) error
	AddLabels(ctx context.Context, repo ghclient.Repo, number int, add []string) error
	RemoveLabel(ctx context.Context, repo ghclient.Repo, number int, name string) error
	Comment(ctx context.Context, repo ghclient.Repo, number int, body string) error
	Assign(ctx context.Context, repo ghclient.Repo, number int, logins []string) error
	Close(ctx context.Context, repo ghclient.Repo, number int) error
	DismissAlert(ctx context.Context, repo ghclient.Repo, number int, reason, comment string) error
}

// FindingReconciler projects each Finding (and its children's results) onto
// its tracking issue, and owns the accumulation-window condition.
type FindingReconciler struct {
	client.Client
	// Creds builds Integration API clients.
	Creds *Creds
	// Namespace the Findings live in.
	Namespace string
	// Now is the clock seam; nil means time.Now.
	Now func() time.Time
	// ClientFor overrides the tracker-client construction in tests.
	ClientFor func(ctx context.Context, integ *v1alpha1.Integration, repo ghclient.Repo) (trackerClient, error)
	// Log receives diagnostics; nil discards.
	Log *slog.Logger
}

// Reconcile implements the projection control loop.
func (r *FindingReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var fnd v1alpha1.Finding
	if err := r.Get(ctx, req.NamespacedName, &fnd); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !fnd.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	requeue, err := r.closeAccumulation(ctx, &fnd)
	if err != nil {
		return ctrl.Result{}, err
	}

	if err := r.project(ctx, &fnd); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeue}, nil
}

// closeAccumulation flips AccumulationComplete once the window elapses,
// returning the remaining wait when it has not.
func (r *FindingReconciler) closeAccumulation(ctx context.Context, fnd *v1alpha1.Finding) (time.Duration, error) {
	if meta.IsStatusConditionTrue(fnd.Status.Conditions, v1alpha1.ConditionAccumulationComplete) {
		return 0, nil
	}
	until := fnd.Status.AccumulateUntil
	if until == nil {
		return 0, nil // ingest still initializing status; its update re-queues us
	}
	if wait := until.Sub(r.now()); wait > 0 {
		return wait, nil
	}
	meta.SetStatusCondition(&fnd.Status.Conditions, metav1.Condition{
		Type:               v1alpha1.ConditionAccumulationComplete,
		Status:             metav1.ConditionTrue,
		Reason:             "WindowElapsed",
		ObservedGeneration: fnd.Generation,
	})
	if err := r.Status().Update(ctx, fnd); err != nil {
		if kerrors.IsConflict(err) {
			return time.Second, nil // re-Get on the requeue
		}
		return 0, err
	}
	return 0, nil
}

// project pushes the Finding's current state to its tracking issue.
func (r *FindingReconciler) project(ctx context.Context, fnd *v1alpha1.Finding) error {
	if fnd.Spec.TrackingRef == nil || fnd.Spec.Repository == nil {
		return nil // nothing to project onto (no tracker, or repo-less finding)
	}
	var integ v1alpha1.Integration
	key := types.NamespacedName{Namespace: fnd.Namespace, Name: fnd.Spec.TrackingRef.Name}
	if err := r.Get(ctx, key, &integ); err != nil {
		return client.IgnoreNotFound(err)
	}
	if !issuesEnabled(&integ) {
		return nil
	}
	repo, err := parseOwnerRepo(fnd.Spec.Repository.Name)
	if err != nil {
		return nil // malformed spec; nothing sane to project
	}
	tracker, err := r.clientFor(ctx, &integ, repo)
	if err != nil {
		return fmt.Errorf("tracker client: %w", err)
	}
	if fnd.Status.Tracking == nil {
		return r.createIssue(ctx, fnd, &integ, tracker, repo)
	}
	if err := r.rerender(ctx, fnd, tracker, repo); err != nil {
		return r.unlinkIfGone(ctx, fnd, err)
	}
	number := int(fnd.Status.Tracking.IssueNumber)
	if err := r.projectComments(ctx, fnd, tracker, repo, number); err != nil {
		return r.unlinkIfGone(ctx, fnd, err)
	}
	if err := r.projectPhase(ctx, fnd, tracker, repo, number); err != nil {
		return r.unlinkIfGone(ctx, fnd, err)
	}
	return nil
}

// unlinkIfGone handles a projection error: a 404 means the tracking issue
// no longer exists on the forge (deleted there, or an ephemeral tracker was
// reset), so the dead link is dropped from status and the next reconcile
// projects a fresh issue. Any other error is returned unchanged.
func (r *FindingReconciler) unlinkIfGone(ctx context.Context, fnd *v1alpha1.Finding, cause error) error {
	if !ghclient.IsNotFound(cause) {
		return cause
	}
	if r.Log != nil {
		r.Log.LogAttrs(ctx, slog.LevelWarn, "tracking issue gone; reprojecting",
			slog.String("finding", fnd.Name),
			slog.Int64("issue", fnd.Status.Tracking.IssueNumber))
	}
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var cur v1alpha1.Finding
		if err := r.Get(ctx, client.ObjectKeyFromObject(fnd), &cur); err != nil {
			return client.IgnoreNotFound(err)
		}
		cur.Status.Tracking = nil
		if err := r.Status().Update(ctx, &cur); err != nil {
			return err
		}
		fnd.Status.Tracking = nil
		return nil
	})
	if err != nil {
		return fmt.Errorf("unlink dead tracking issue: %w", err)
	}
	return nil
}

// createIssue opens the tracking issue and links it into status.tracking.
func (r *FindingReconciler) createIssue(
	ctx context.Context, fnd *v1alpha1.Finding, integ *v1alpha1.Integration,
	tracker trackerClient, repo ghclient.Repo,
) error {
	body, err := templates.RenderFindingIssue(fnd)
	if err != nil {
		return err
	}
	desired := projectedLabels(fnd)
	issue, err := tracker.Create(ctx, repo, ghclient.IssueRequest{
		Title:  templates.FindingIssueTitle(fnd),
		Body:   body,
		Labels: desired.Render(),
	})
	if err != nil {
		return fmt.Errorf("create tracking issue: %w", err)
	}
	// The issue exists; the link write must survive status contention — a
	// dropped link would make the next reconcile duplicate the issue, so
	// re-Get and retry conflicts here rather than surfacing them.
	tracking := &v1alpha1.TrackingStatus{
		Integration: integ.Name,
		IssueNumber: int64(issue.Number),
		URL:         issueURL(integ, repo, issue.Number),
		State:       "open",
	}
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var cur v1alpha1.Finding
		if err := r.Get(ctx, client.ObjectKeyFromObject(fnd), &cur); err != nil {
			return client.IgnoreNotFound(err)
		}
		if cur.Status.Tracking == nil {
			cur.Status.Tracking = tracking
		}
		if err := r.Status().Update(ctx, &cur); err != nil {
			return err
		}
		fnd.Status.Tracking = cur.Status.Tracking
		return nil
	})
	if err != nil {
		return fmt.Errorf("link tracking issue: %w", err)
	}
	return r.markProjected(ctx, fnd, map[string]string{
		AnnotationProjectedBody: hashOf(body + strings.Join(desired.Render(), "|")),
	})
}

// rerender pushes body and label changes when the projected hash moved.
func (r *FindingReconciler) rerender(
	ctx context.Context, fnd *v1alpha1.Finding, tracker trackerClient, repo ghclient.Repo,
) error {
	body, err := templates.RenderFindingIssue(fnd)
	if err != nil {
		return err
	}
	desired := projectedLabels(fnd)
	number := int(fnd.Status.Tracking.IssueNumber)
	rendered := hashOf(body + strings.Join(desired.Render(), "|"))
	if fnd.GetAnnotations()[AnnotationProjectedBody] == rendered {
		return nil
	}
	issue, err := tracker.GetIssue(ctx, repo, number)
	if err != nil {
		return fmt.Errorf("get tracking issue: %w", err)
	}
	if issue.Body != body {
		if err := tracker.EditBody(ctx, repo, number, body); err != nil {
			return err
		}
	}
	add, remove := labels.Diff(labels.Parse(issue.Labels), desired)
	if len(add) > 0 {
		if err := tracker.AddLabels(ctx, repo, number, add); err != nil {
			return err
		}
	}
	for _, l := range remove {
		if err := tracker.RemoveLabel(ctx, repo, number, l); err != nil {
			return err
		}
	}
	return r.markProjected(ctx, fnd, map[string]string{AnnotationProjectedBody: rendered})
}

// projectComments posts enrichment and report comments not yet projected.
func (r *FindingReconciler) projectComments(
	ctx context.Context, fnd *v1alpha1.Finding, tracker trackerClient, repo ghclient.Repo, number int,
) error {
	ann := fnd.GetAnnotations()

	posted, _ := strconv.Atoi(ann[AnnotationProjectedEnrichments])
	for i := posted; i < len(fnd.Status.Enrichments); i++ {
		e := fnd.Status.Enrichments[i]
		if e.Markdown != "" {
			if err := tracker.Comment(ctx, repo, number, templates.RenderEnrichmentProjection(e)); err != nil {
				return err
			}
		}
		if err := r.markProjected(ctx, fnd, map[string]string{
			AnnotationProjectedEnrichments: strconv.Itoa(i + 1),
		}); err != nil {
			return err
		}
	}

	if inv := fnd.Status.Investigation; inv != nil && ann[AnnotationProjectedInvestigation] != inv.Name {
		var child v1alpha1.Investigation
		key := types.NamespacedName{Namespace: fnd.Namespace, Name: inv.Name}
		if err := r.Get(ctx, key, &child); err == nil && child.Status.Report != "" {
			comment := templates.RenderStageReportComment("Investigation", inv.Attempt, child.Status.Report)
			if err := tracker.Comment(ctx, repo, number, comment); err != nil {
				return err
			}
		}
		if err := r.markProjected(ctx, fnd, map[string]string{AnnotationProjectedInvestigation: inv.Name}); err != nil {
			return err
		}
	}

	if rem := fnd.Status.Remediation; rem != nil && ann[AnnotationProjectedRemediation] != rem.Name {
		var child v1alpha1.Remediation
		key := types.NamespacedName{Namespace: fnd.Namespace, Name: rem.Name}
		if err := r.Get(ctx, key, &child); err == nil && child.Status.Report != "" {
			comment := templates.RenderStageReportComment("Remediation", rem.Attempt, child.Status.Report)
			if err := tracker.Comment(ctx, repo, number, comment); err != nil {
				return err
			}
		}
		if err := r.markProjected(ctx, fnd, map[string]string{AnnotationProjectedRemediation: rem.Name}); err != nil {
			return err
		}
	}
	return nil
}

// projectPhase performs the phase-driven tracking actions: notices and
// assignment for human-owned phases, alert dismissal and closure for
// Dismissed, closure for Remediated.
func (r *FindingReconciler) projectPhase(
	ctx context.Context, fnd *v1alpha1.Finding, tracker trackerClient, repo ghclient.Repo, number int,
) error {
	ann := fnd.GetAnnotations()
	phase := fnd.Status.Phase
	if ann[AnnotationProjectedNotice] == string(phase) {
		return nil
	}

	switch phase {
	case v1alpha1.PhaseAwaitingApproval:
		notice := "patchy is holding this remediation for human approval. " +
			"Comment `" + r.approveCommand(ctx, fnd) + "` to let it proceed."
		if err := r.notify(ctx, tracker, repo, number, fnd.Status.Owners, notice); err != nil {
			return err
		}
	case v1alpha1.PhaseHandedOff:
		notice := "patchy has handed this finding to its human owners" + noticeReason(fnd) + "."
		if err := r.notify(ctx, tracker, repo, number, fnd.Status.Owners, notice); err != nil {
			return err
		}
	case v1alpha1.PhaseFailed:
		notice := "patchy could not remediate this finding automatically; it needs human attention."
		if err := r.notify(ctx, tracker, repo, number, fnd.Status.Owners, notice); err != nil {
			return err
		}
	case v1alpha1.PhaseDismissed:
		if err := r.dismissAlerts(ctx, fnd); err != nil {
			return err
		}
		if err := tracker.Comment(ctx, repo, number,
			"patchy assessed this finding as a false positive / not exploitable and dismissed the alert(s)."); err != nil {
			return err
		}
		if err := tracker.Close(ctx, repo, number); err != nil {
			return err
		}
		r.setTrackingState(ctx, fnd, "closed")
	case v1alpha1.PhaseRemediated:
		if err := tracker.Close(ctx, repo, number); err != nil {
			return err
		}
		r.setTrackingState(ctx, fnd, "closed")
	default:
		return nil
	}
	return r.markProjected(ctx, fnd, map[string]string{AnnotationProjectedNotice: string(phase)})
}

// notify posts a notice comment and assigns owners.
func (r *FindingReconciler) notify(
	ctx context.Context, tracker trackerClient, repo ghclient.Repo, number int, owners []string, notice string,
) error {
	if err := tracker.Comment(ctx, repo, number, notice); err != nil {
		return err
	}
	if len(owners) > 0 {
		if err := tracker.Assign(ctx, repo, number, owners); err != nil {
			return err
		}
	}
	return nil
}

// dismissAlerts dismisses every source alert via the code-scanning
// Integration (which may be a different object than the tracking one).
func (r *FindingReconciler) dismissAlerts(ctx context.Context, fnd *v1alpha1.Finding) error {
	integ, err := selectIntegration(ctx, r.Client, fnd.Namespace, codeScanningEnabled)
	if err != nil {
		if errors.Is(err, ErrNoIntegration) {
			return nil
		}
		return err
	}
	repo, err := parseOwnerRepo(fnd.Spec.Repository.Name)
	if err != nil {
		return nil
	}
	scanner, err := r.clientFor(ctx, integ, repo)
	if err != nil {
		return err
	}
	reason := "false positive"
	comment := "Dismissed by patchy: investigation recommended ignore."
	var errs []error
	for _, a := range fnd.Spec.Alerts {
		num, err := strconv.Atoi(a.ID)
		if err != nil {
			continue // foreign-source alert id; nothing to dismiss on GitHub
		}
		if err := scanner.DismissAlert(ctx, repo, num, reason, comment); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// setTrackingState updates status.tracking.state under conflict retry,
// best-effort (the issue webhook also maintains it).
func (r *FindingReconciler) setTrackingState(ctx context.Context, fnd *v1alpha1.Finding, state string) {
	_ = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var cur v1alpha1.Finding
		if err := r.Get(ctx, client.ObjectKeyFromObject(fnd), &cur); err != nil {
			return client.IgnoreNotFound(err)
		}
		if cur.Status.Tracking == nil || cur.Status.Tracking.State == state {
			return nil
		}
		cur.Status.Tracking.State = state
		return r.Status().Update(ctx, &cur)
	})
}

// markProjected merges projection-state annotations onto the Finding under
// conflict retry.
func (r *FindingReconciler) markProjected(ctx context.Context, fnd *v1alpha1.Finding, set map[string]string) error {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var cur v1alpha1.Finding
		if err := r.Get(ctx, client.ObjectKeyFromObject(fnd), &cur); err != nil {
			return client.IgnoreNotFound(err)
		}
		ann := cur.GetAnnotations()
		if ann == nil {
			ann = map[string]string{}
		}
		changed := false
		for k, v := range set {
			if ann[k] != v {
				ann[k] = v
				changed = true
			}
		}
		if !changed {
			return nil
		}
		cur.SetAnnotations(ann)
		if err := r.Update(ctx, &cur); err != nil {
			return err
		}
		fnd.SetAnnotations(ann)
		return nil
	})
	return err
}

// projectedLabels is the trimmed human-facing label vocabulary rendered from
// the Finding.
func projectedLabels(fnd *v1alpha1.Finding) labels.Set {
	s := labels.Set{
		Source:     fnd.Spec.Source,
		Advisories: fnd.Spec.Advisories,
		Finding:    labels.State(phaseLabel(fnd.Status.Phase)),
		Severity:   labels.Level(fnd.Spec.Severity),
		Priority:   labels.Level(fnd.Status.Priority),
	}
	if inv := fnd.Status.Investigation; inv != nil {
		s.Recommendation = labels.Recommendation(inv.Recommendation)
	}
	return s
}

// phaseLabel is the kebab-case label value of a phase.
func phaseLabel(p v1alpha1.Phase) string {
	var b strings.Builder
	for i, r := range string(p) {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r - 'A' + 'a')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// approveCommand is the integration's configured approve comment.
func (r *FindingReconciler) approveCommand(ctx context.Context, fnd *v1alpha1.Finding) string {
	var integ v1alpha1.Integration
	if fnd.Spec.TrackingRef != nil {
		key := types.NamespacedName{Namespace: fnd.Namespace, Name: fnd.Spec.TrackingRef.Name}
		if err := r.Get(ctx, key, &integ); err == nil &&
			integ.Spec.GitHub != nil && integ.Spec.GitHub.Issues != nil &&
			integ.Spec.GitHub.Issues.ApproveComment != "" {
			return integ.Spec.GitHub.Issues.ApproveComment
		}
	}
	return "/approve"
}

// noticeReason explains a hand-off from the finding's state.
func noticeReason(fnd *v1alpha1.Finding) string {
	if inv := fnd.Status.Investigation; inv != nil && inv.Recommendation == v1alpha1.RecommendationManual {
		return " (investigation recommended a manual fix)"
	}
	if fnd.Spec.Repository == nil {
		return " (no repository could be identified)"
	}
	return ""
}

// issueURL derives the tracking item's html URL.
func issueURL(integ *v1alpha1.Integration, repo ghclient.Repo, number int) string {
	return fmt.Sprintf("https://%s/%s/%s/issues/%d", githubHost(integ), repo.Owner, repo.Name, number)
}

// parseOwnerRepo splits "owner/name".
func parseOwnerRepo(full string) (ghclient.Repo, error) {
	owner, name, ok := strings.Cut(full, "/")
	if !ok || owner == "" || name == "" {
		return ghclient.Repo{}, fmt.Errorf("malformed repository name %q", full)
	}
	return ghclient.Repo{Owner: owner, Name: name}, nil
}

// clientFor resolves the tracker-client seam.
func (r *FindingReconciler) clientFor(
	ctx context.Context, integ *v1alpha1.Integration, repo ghclient.Repo,
) (trackerClient, error) {
	if r.ClientFor != nil {
		return r.ClientFor(ctx, integ, repo)
	}
	return r.Creds.Client(ctx, integ, repo)
}

// SetupWithManager wires the reconciler: Findings plus child watches mapped
// to the owning Finding, and the tracking-URL field index the Signals
// handler queries.
func (r *FindingReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &v1alpha1.Finding{}, TrackingURLIndex,
		func(obj client.Object) []string {
			f := obj.(*v1alpha1.Finding)
			if f.Status.Tracking == nil || f.Status.Tracking.URL == "" {
				return nil
			}
			return []string{f.Status.Tracking.URL}
		}); err != nil {
		return fmt.Errorf("index %s: %w", TrackingURLIndex, err)
	}
	mapChild := handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []ctrl.Request {
		name := obj.GetLabels()[v1alpha1.LabelFinding]
		if name == "" {
			return nil
		}
		return []ctrl.Request{{NamespacedName: types.NamespacedName{Namespace: obj.GetNamespace(), Name: name}}}
	})
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Finding{}).
		Watches(&v1alpha1.Investigation{}, mapChild).
		Watches(&v1alpha1.Remediation{}, mapChild).
		Named("finding-projection").
		Complete(r)
}

func (r *FindingReconciler) now() time.Time {
	if r.Now == nil {
		return time.Now()
	}
	return r.Now()
}
