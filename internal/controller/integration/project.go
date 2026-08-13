// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package integration

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/generic"
	"github.com/bitwise-media-group/patchy/internal/ghas"
	"github.com/bitwise-media-group/patchy/internal/ghclient"
	"github.com/bitwise-media-group/patchy/internal/labels"
	"github.com/bitwise-media-group/patchy/internal/report"
	"github.com/bitwise-media-group/patchy/internal/templates"
	"github.com/bitwise-media-group/patchy/internal/wiz"
	"github.com/bitwise-media-group/patchy/internal/wizapi"
	"github.com/bitwise-media-group/patchy/pkg/source"
)

// Projection-state annotations on the Finding: what has already been pushed
// to the tracking item, so re-reconciles stay idempotent without re-reading
// comments. All are integration-controller-owned.
const (
	// AnnotationProjectedBody is the hash of the last rendered body+labels.
	AnnotationProjectedBody = "patchy.bitwisemedia.uk/projected-body"
	// AnnotationProjectedEnrichments is the hash of the last projected
	// enrichment sticky comments.
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
	// AnnotationResolvedSource records the phase whose verdict was written
	// back to the originating source. Separate from the notice annotation on
	// purpose: telling the scanner and telling the humans are independent, so
	// a failure of either must not suppress the other.
	AnnotationResolvedSource = "patchy.bitwisemedia.uk/resolved-source"
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
	ListComments(ctx context.Context, repo ghclient.Repo, number int) ([]*ghclient.Comment, error)
	EditComment(ctx context.Context, repo ghclient.Repo, commentID int64, body string) error
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
	// Concurrency is the number of findings projected in parallel; <=1 means
	// serial. Kept conservative by default — every projection may spend
	// GitHub API requests, and parallelism multiplies burst pressure on the
	// installation's rate limit.
	Concurrency int
	// Now is the clock seam; nil means time.Now.
	Now func() time.Time
	// ClientFor overrides the tracker-client construction in tests.
	ClientFor func(ctx context.Context, integ *v1alpha1.Integration, repo ghclient.Repo) (trackerClient, error)
	// WizAPI overrides the Wiz write-back client construction in tests.
	WizAPI func(ctx context.Context, integ *v1alpha1.Integration) (wiz.IssueRejecter, error)
	// GenericResolver overrides the generic write-back construction in tests.
	GenericResolver func(ctx context.Context, integ *v1alpha1.Integration) (source.Resolver, error)
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

	// The verdict write-back is independent of the tracking projection: a
	// finding with no tracking issue — repo-less, or with tracking disabled
	// entirely — must still have its alerts resolved in the tool they came
	// from, or they sit open there after patchy has closed them here.
	if err := r.resolveSource(ctx, &fnd); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.project(ctx, &fnd); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeue}, nil
}

// resolveSource writes the pipeline's verdict back to the originating
// sources, once per phase entry.
func (r *FindingReconciler) resolveSource(ctx context.Context, fnd *v1alpha1.Finding) error {
	phase := fnd.Status.Phase
	if phase != v1alpha1.PhaseDismissed {
		return nil // only an ignore verdict has anything to say
	}
	if fnd.GetAnnotations()[AnnotationResolvedSource] == string(phase) {
		return nil
	}
	if err := r.resolveAlerts(ctx, fnd); err != nil {
		return err
	}
	return r.markProjected(ctx, fnd, map[string]string{AnnotationResolvedSource: string(phase)})
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
//
// The guard lives here, not in Reconcile: closeAccumulation runs first and is
// the only writer of the AccumulationComplete condition the investigation
// gate blocks on. Skipping this function is safe; skipping the reconcile is
// not — it would strand every deferred finding at Enhanced, silently.
func (r *FindingReconciler) project(ctx context.Context, fnd *v1alpha1.Finding) error {
	if fnd.Spec.TrackingRef == nil {
		return nil // no tracker configured
	}
	// A cloud finding's repository is resolved by the enhancer chain, which
	// runs at Opened. Projecting before then would open the issue on the
	// fallback repository and strand it there — an issue cannot move once
	// created, so waiting one phase is the difference between the right
	// repository and a permanent misfile.
	if fnd.Spec.CloudResource != nil && fnd.Spec.Repository == nil &&
		fnd.Status.Phase == v1alpha1.PhaseOpened {
		return nil
	}
	var integ v1alpha1.Integration
	key := types.NamespacedName{Namespace: fnd.Namespace, Name: fnd.Spec.TrackingRef.Name}
	if err := r.Get(ctx, key, &integ); err != nil {
		return client.IgnoreNotFound(err)
	}
	if !issuesEnabled(&integ) {
		return nil
	}
	repo, err := trackingRepo(fnd, &integ)
	if err != nil {
		return nil // nowhere sane to project
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

// trackingRepo picks where the finding's issue lives: its own repository, or
// the tracking Integration's fallback when it has none.
//
// The fallback is what makes a repo-less cloud finding visible to the humans
// who triage it. Without one such a finding exists only in kubectl and the
// status page — it can still be handed off, but to nobody who will see it.
func trackingRepo(fnd *v1alpha1.Finding, integ *v1alpha1.Integration) (ghclient.Repo, error) {
	if fnd.Spec.Repository != nil {
		return parseOwnerRepo(fnd.Spec.Repository.Name)
	}
	if integ.Spec.GitHub != nil && integ.Spec.GitHub.Issues != nil {
		if fallback := integ.Spec.GitHub.Issues.FallbackRepository; fallback != "" {
			return parseOwnerRepo(fallback)
		}
	}
	return ghclient.Repo{}, errNoTrackingRepo
}

// errNoTrackingRepo: the finding has no repository and the tracking
// Integration configures no fallback, so there is nowhere to open an issue.
var errNoTrackingRepo = errors.New("no repository and no fallback repository")

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

// projectComments keeps the enrichment sticky comments current and posts
// report comments not yet projected.
func (r *FindingReconciler) projectComments(
	ctx context.Context, fnd *v1alpha1.Finding, tracker trackerClient, repo ghclient.Repo, number int,
) error {
	ann := fnd.GetAnnotations()

	if err := r.projectEnrichments(ctx, fnd, tracker, repo, number); err != nil {
		return err
	}

	if inv := fnd.Status.Investigation; inv != nil && ann[AnnotationProjectedInvestigation] != inv.Name {
		var child v1alpha1.Investigation
		key := types.NamespacedName{Namespace: fnd.Namespace, Name: inv.Name}
		if err := r.Get(ctx, key, &child); err == nil && child.Status.Report != "" {
			// Status.Report carries the machine frontmatter; comments are
			// presentation, so render the markdown body only.
			comment := templates.RenderStageReportComment("Investigation", inv.Attempt,
				report.StripFrontmatter(child.Status.Report))
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
			comment := templates.RenderStageReportComment("Remediation", rem.Attempt,
				report.StripFrontmatter(child.Status.Report))
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

// projectEnrichments keeps one sticky comment per enhancer up to date: an
// enrichment's markdown lands in a marker-headed comment that is edited in
// place when the content moves, never re-posted. Attributes carry no comment
// — they project as labels via projectedLabels.
func (r *FindingReconciler) projectEnrichments(
	ctx context.Context, fnd *v1alpha1.Finding, tracker trackerClient, repo ghclient.Repo, number int,
) error {
	var desired []string
	for _, e := range fnd.Status.Enrichments {
		if e.Markdown != "" {
			desired = append(desired, templates.RenderEnrichmentProjection(e))
		}
	}
	var state string
	if len(desired) > 0 {
		state = hashOf(strings.Join(desired, "\x00"))
	}
	if fnd.GetAnnotations()[AnnotationProjectedEnrichments] == state {
		return nil
	}
	var existing []*ghclient.Comment
	if len(desired) > 0 {
		var err error
		if existing, err = tracker.ListComments(ctx, repo, number); err != nil {
			return err
		}
	}
	for _, body := range desired {
		marker, _, _ := strings.Cut(body, "\n")
		sticky := findSticky(existing, marker)
		switch {
		case sticky == nil:
			if err := tracker.Comment(ctx, repo, number, body); err != nil {
				return err
			}
		case sticky.Body != body:
			if err := tracker.EditComment(ctx, repo, sticky.ID, body); err != nil {
				return err
			}
		}
	}
	return r.markProjected(ctx, fnd, map[string]string{AnnotationProjectedEnrichments: state})
}

// findSticky returns the first comment headed by marker, or nil.
func findSticky(comments []*ghclient.Comment, marker string) *ghclient.Comment {
	for _, c := range comments {
		if c.Body == marker || strings.HasPrefix(c.Body, marker+"\n") {
			return c
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
		// The alert write-back happens in resolveSource, before projection:
		// it is owed whether or not this finding has a tracking issue.
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

// resolveAlerts tells each source what the pipeline decided about its own
// alerts — GHAS dismisses them, a cloud source would mute them.
//
// Alerts are grouped by the source that produced them and handed to that
// source's resolver, so provenance is a recorded fact rather than something
// inferred from the shape of an id. A source that implements no write-back is
// simply skipped: reading is a complete source.
func (r *FindingReconciler) resolveAlerts(ctx context.Context, fnd *v1alpha1.Finding) error {
	verdict := source.Verdict{
		Kind:    source.VerdictIgnore,
		Reason:  "false positive",
		Comment: "Dismissed by patchy: investigation recommended ignore.",
	}
	var errs []error
	for sourceID, alerts := range fnd.AlertsBySource() {
		resolver, err := r.resolverFor(ctx, fnd, sourceID)
		if err != nil {
			if errors.Is(err, ErrNoIntegration) {
				continue // the source's integration is gone; nothing to tell
			}
			errs = append(errs, err)
			continue
		}
		if resolver == nil {
			continue // this source has no write-back
		}
		if err := resolver.Resolve(ctx, toAlertRefs(alerts), verdict); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// resolverFor builds the write-back for one source, or nil when that source
// has none. It dispatches on the source id recorded at ingest rather than on
// a capability predicate, so a second source is a new case here and nothing
// more.
func (r *FindingReconciler) resolverFor(
	ctx context.Context, fnd *v1alpha1.Finding, sourceID string,
) (source.Resolver, error) {
	switch sourceID {
	case ghas.ID:
		integ, err := selectIntegration(ctx, r.Client, fnd.Namespace, codeScanningEnabled)
		if err != nil {
			return nil, err
		}
		// A repo-less finding has nothing on GitHub to dismiss; the resolver
		// handles the zero repo, so this need not special-case it.
		var repo ghclient.Repo
		if fnd.Spec.Repository != nil {
			repo, _ = parseOwnerRepo(fnd.Spec.Repository.Name)
		}
		gh, err := r.clientFor(ctx, integ, repo)
		if err != nil {
			return nil, err
		}
		return ghas.NewResolver(gh, repo), nil
	case wiz.IssuesID:
		integ, err := selectIntegration(ctx, r.Client, fnd.Namespace, wizIssuesEnabled)
		if err != nil {
			return nil, err
		}
		if integ.Spec.Wiz.API == nil {
			return nil, nil // write-back not configured; ingestion stays one-way
		}
		api, err := r.wizClientFor(ctx, integ)
		if err != nil {
			return nil, err
		}
		return wiz.NewResolver(api), nil
	default:
		// Security Command Center findings would be muted here once
		// integration-controller holds a Google Cloud write credential; the
		// seam is in place and internal/scc implements source.Handler only.
		// Wiz Defend threats have no write-back by design: a runtime
		// detection is a record, not a state to dismiss.
		//
		// Any other source id is a generic integration's name — that is
		// what makes generic write-back a lookup rather than a new case.
		return r.genericResolverFor(ctx, fnd, sourceID)
	}
}

// genericResolverFor builds the write-back for a generic source, or nil when
// the named Integration is gone, is not generic, or has the resolver off —
// exactly the "reading is a complete source" posture of the typed providers.
func (r *FindingReconciler) genericResolverFor(
	ctx context.Context, fnd *v1alpha1.Finding, sourceID string,
) (source.Resolver, error) {
	var integ v1alpha1.Integration
	key := types.NamespacedName{Namespace: fnd.Namespace, Name: sourceID}
	if err := r.Get(ctx, key, &integ); err != nil {
		if kerrors.IsNotFound(err) {
			return nil, nil // an unknown source id has no write-back
		}
		return nil, fmt.Errorf("get generic integration %s: %w", key, err)
	}
	if !genericResolverEnabled(&integ) {
		return nil, nil
	}
	if r.GenericResolver != nil {
		return r.GenericResolver(ctx, &integ)
	}
	secret, err := r.Creds.WebhookSecret(ctx, &integ)
	if err != nil {
		return nil, err
	}
	res := integ.Spec.Generic.Source.Resolver
	c, err := generic.NewClient(generic.ClientOptions{
		URL:     res.URL,
		Secret:  secret,
		Timeout: res.Timeout.Duration,
	})
	if err != nil {
		return nil, err
	}
	return generic.NewResolver(c, integ.Name), nil
}

// wizClientFor resolves the Wiz write-back seam, honouring the test override.
func (r *FindingReconciler) wizClientFor(
	ctx context.Context, integ *v1alpha1.Integration,
) (wiz.IssueRejecter, error) {
	if r.WizAPI != nil {
		return r.WizAPI(ctx, integ)
	}
	id, secret, err := r.Creds.WizAPICreds(ctx, integ)
	if err != nil {
		return nil, err
	}
	return wizapi.New(wizapi.Options{
		Endpoint:     integ.Spec.Wiz.API.Endpoint,
		TokenURL:     integ.Spec.Wiz.API.TokenURL,
		ClientID:     id,
		ClientSecret: secret,
	})
}

// toAlertRefs adapts the CR's alerts to the seam's shape.
func toAlertRefs(alerts []v1alpha1.Alert) []source.AlertRef {
	out := make([]source.AlertRef, 0, len(alerts))
	for _, a := range alerts {
		out = append(out, source.AlertRef{ID: a.ID, Source: a.Source, URL: a.URL})
	}
	return out
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
	// Enrichment attributes, first enhancer wins on a key collision (the
	// chain runs most-authoritative first, matching owner precedence).
	for _, e := range fnd.Status.Enrichments {
		for k, v := range e.Attributes {
			if _, ok := s.Context[k]; ok {
				continue
			}
			if s.Context == nil {
				s.Context = map[string]string{}
			}
			s.Context[k] = v
		}
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
		WithOptions(controller.Options{MaxConcurrentReconciles: max(1, r.Concurrency)}).
		Named("finding-projection").
		Complete(r)
}

func (r *FindingReconciler) now() time.Time {
	if r.Now == nil {
		return time.Now()
	}
	return r.Now()
}
