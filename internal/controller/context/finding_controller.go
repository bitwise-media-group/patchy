// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package context

import (
	"context"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/pkg/enhance"
	"github.com/bitwise-media-group/patchy/pkg/source"
)

// maxEnrichmentMarkdown caps one enhancer's markdown on the Finding.
const maxEnrichmentMarkdown = 16384

// defaultRetryAfter paces retries of a cloud finding's repository lookup.
// Short enough that a brief API outage costs little, long enough that a
// sustained one is not a hot loop; the accumulation window bounds the total.
const defaultRetryAfter = time.Minute

// FindingReconciler runs the enhancer chain over freshly opened Findings —
// the CRD-native context-controller. It writes Finding status (enrichments,
// owners, the ContextEnhanced condition, and the Opened→Enhanced edge) and
// exactly one spec field: spec.repository, set once for a cloud finding that
// arrived without one. It holds no tracking-system credential — comments are
// integration-controller projection work — though an enhancer may hold a
// read-only credential for whatever it looks findings up in.
type FindingReconciler struct {
	client.Client
	// Enhancers run in order; each contributes at most one enrichment.
	Enhancers []enhance.Enhancer
	// RetryAfter paces retries of a cloud finding's repository lookup; zero
	// means defaultRetryAfter.
	RetryAfter time.Duration
	// Now is the clock seam; nil means time.Now.
	Now func() time.Time
	// Log receives diagnostics; nil discards.
	Log *slog.Logger
}

// Reconcile enhances one Finding.
func (r *FindingReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var fnd v1alpha1.Finding
	if err := r.Get(ctx, req.NamespacedName, &fnd); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !fnd.DeletionTimestamp.IsZero() || fnd.Spec.Suspend || fnd.Status.Phase != v1alpha1.PhaseOpened {
		return ctrl.Result{}, nil
	}

	result := r.runChain(ctx, &fnd)

	// A cloud finding whose repository lookup errored gets another try. The
	// chain runs exactly once — there is no transition back to Opened — so
	// advancing now would lose the repository permanently, and a repo-less
	// finding is terminally handed off. Holding at Opened is cheap: alerts
	// still fold in, and the accumulation window still closes on its own.
	if r.holdForRepository(&fnd, result.repository, result.failed) {
		return r.hold(ctx, &fnd)
	}

	if repo := toFindingRepository(result.repository); repo != nil && fnd.Spec.Repository == nil {
		// SET-ONCE. The rollup ledger, the clone artifact, and the agent Jobs
		// each snapshot the repository independently; revising it later
		// desynchronizes them silently. See FindingSpec.Repository.
		if err := r.setRepository(ctx, &fnd, repo); err != nil {
			return requeueOnConflict(err)
		}
	}

	fnd.Status.Enrichments = result.enrichments
	fnd.Status.Owners = result.owners
	meta.SetStatusCondition(&fnd.Status.Conditions, contextCondition(&fnd))
	if err := v1alpha1.SetPhase(&fnd, v1alpha1.PhaseEnhanced, r.now()); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.Status().Update(ctx, &fnd); err != nil {
		return requeueOnConflict(err)
	}
	r.log().LogAttrs(ctx, slog.LevelInfo, "finding enhanced",
		slog.String("finding", fnd.Name),
		slog.Int("enrichments", len(result.enrichments)),
		slog.Int("owners", len(result.owners)))
	return ctrl.Result{}, nil
}

// chainResult is what one pass of the enhancer chain produced.
type chainResult struct {
	enrichments []v1alpha1.Enrichment
	owners      []string
	// repository is the first repository any enhancer resolved, for a finding
	// that arrived without one.
	repository *source.RepositoryRef
	// failed records that at least one enhancer errored, which for a cloud
	// finding is the difference between "no repository exists" and "we could
	// not find out".
	failed bool
}

// runChain runs every enhancer over the finding, collecting what they
// contribute. One broken enhancer must not wedge the pipeline, so an error is
// logged and the chain continues; the caller decides what a failure means.
func (r *FindingReconciler) runChain(ctx context.Context, fnd *v1alpha1.Finding) chainResult {
	issue := enhanceInput(fnd)
	var out chainResult
	for _, e := range r.Enhancers {
		enr, err := e.Enhance(ctx, issue)
		if err != nil {
			out.failed = true
			r.log().LogAttrs(ctx, slog.LevelWarn, "enhancer failed",
				slog.String("enhancer", e.ID()),
				slog.String("finding", fnd.Name),
				slog.Any("error", err))
			continue
		}
		if enr == nil {
			continue
		}
		// First enhancer to name a repository wins, matching how the
		// projection resolves colliding attributes.
		if out.repository == nil && enr.Repository != nil {
			out.repository = enr.Repository
		}
		if len(out.enrichments) < 8 {
			out.enrichments = append(out.enrichments, v1alpha1.Enrichment{
				Enhancer:   e.ID(),
				Owners:     enr.Owners,
				Attributes: enr.Attributes,
				Markdown:   truncate(enr.CommentMarkdown, maxEnrichmentMarkdown),
				AppliedAt:  metav1.NewTime(r.now()),
			})
		}
		for _, o := range enr.Owners {
			if !slices.Contains(out.owners, o) {
				out.owners = append(out.owners, o)
			}
		}
	}
	return out
}

// requeueOnConflict turns a write conflict into a requeue — something else
// wrote the finding first, so the next pass re-reads it and decides again.
func requeueOnConflict(err error) (ctrl.Result, error) {
	if kerrors.IsConflict(err) {
		return ctrl.Result{Requeue: true}, nil
	}
	return ctrl.Result{}, err
}

// contextCondition reports how the chain went. A cloud finding that reaches
// Enhanced with no repository is not an error — most resources carry no
// ownership labels — but the reason records why it will be handed off, so the
// answer is on the object rather than only in a log line.
func contextCondition(fnd *v1alpha1.Finding) metav1.Condition {
	c := metav1.Condition{
		Type:               v1alpha1.ConditionContextEnhanced,
		Status:             metav1.ConditionTrue,
		Reason:             "EnhancerChainComplete",
		ObservedGeneration: fnd.Generation,
	}
	if fnd.Spec.CloudResource != nil && fnd.Spec.Repository == nil {
		c.Status = metav1.ConditionFalse
		c.Reason = v1alpha1.ReasonRepositoryUnresolved
		c.Message = "no enhancer resolved a repository for " + fnd.Spec.CloudResource.Name
	}
	return c
}

// holdForRepository reports whether to stay at Opened and retry the chain.
// Only a cloud finding that is still repo-less after an enhancer *errored*
// qualifies: an enhancer cleanly reporting "this resource carries no
// ownership labels" is a final answer, and holding on it would deny the
// finding its hand-off forever.
func (r *FindingReconciler) holdForRepository(
	fnd *v1alpha1.Finding, resolved *source.RepositoryRef, failed bool,
) bool {
	if !failed || resolved != nil || fnd.Spec.CloudResource == nil || fnd.Spec.Repository != nil {
		return false
	}
	// Expedited findings are urgent by an operator's explicit decision; the
	// gate skips the accumulation window for them, so holding here would put
	// the delay straight back.
	if fnd.Spec.Expedite != nil {
		return false
	}
	// The bound. Past the accumulation window the finding is ready to be
	// investigated, so a still-broken lookup stops being worth waiting for —
	// better handed off to a human than held out of sight.
	return !meta.IsStatusConditionTrue(fnd.Status.Conditions, v1alpha1.ConditionAccumulationComplete)
}

// hold keeps the finding at Opened and asks to be called back. The phase is
// untouched, so the openedOnly predicate still matches and the requeue is all
// that is needed.
func (r *FindingReconciler) hold(ctx context.Context, fnd *v1alpha1.Finding) (ctrl.Result, error) {
	meta.SetStatusCondition(&fnd.Status.Conditions, metav1.Condition{
		Type:               v1alpha1.ConditionContextEnhanced,
		Status:             metav1.ConditionFalse,
		Reason:             v1alpha1.ReasonRepositoryUnresolved,
		Message:            "repository lookup failed; retrying until the accumulation window closes",
		ObservedGeneration: fnd.Generation,
	})
	if err := r.Status().Update(ctx, fnd); err != nil {
		return requeueOnConflict(err)
	}
	r.log().LogAttrs(ctx, slog.LevelInfo, "holding finding for repository resolution",
		slog.String("finding", fnd.Name),
		slog.String("resource", fnd.Spec.CloudResource.Name))
	return ctrl.Result{RequeueAfter: r.retryAfter()}, nil
}

// setRepository writes the resolved repository onto spec. A conflict means
// something else wrote the finding first, and the caller requeues rather than
// retrying in place: on the next pass spec.repository may already be set, and
// the set-once guard is what must decide, not this function.
//
// This is the one place outside integration-controller that writes Finding
// spec. The exemption is recorded in the admission policy's principal list.
func (r *FindingReconciler) setRepository(
	ctx context.Context, fnd *v1alpha1.Finding, repo *v1alpha1.FindingRepository,
) error {
	fnd.Spec.Repository = repo
	if err := r.Update(ctx, fnd); err != nil {
		return err
	}
	r.log().LogAttrs(ctx, slog.LevelInfo, "repository resolved for cloud finding",
		slog.String("finding", fnd.Name),
		slog.String("resource", fnd.Spec.CloudResource.Name),
		slog.String("repository", repo.Name))
	return nil
}

// toFindingRepository converts an enhancer's answer to the CR shape, or nil
// when it names something the pipeline cannot act on. A forge patchy does not
// support is dropped rather than written: a Finding carrying a repository it
// can never clone would stall at the gate instead of being handed off.
func toFindingRepository(ref *source.RepositoryRef) *v1alpha1.FindingRepository {
	if ref == nil {
		return nil
	}
	if !strings.EqualFold(ref.Provider, string(v1alpha1.RepositoryTypeGitHub)) {
		return nil
	}
	repoURL, name := ref.URL, ""
	if ref.Owner != "" && ref.Name != "" {
		name = ref.Owner + "/" + ref.Name
		if repoURL == "" {
			repoURL = "https://github.com/" + name
		}
	}
	if repoURL == "" {
		return nil
	}
	if name == "" {
		name = repoNameFromURL(repoURL)
	}
	return &v1alpha1.FindingRepository{
		Type: v1alpha1.RepositoryTypeGitHub,
		URL:  repoURL,
		Name: name,
	}
}

// repoNameFromURL recovers "owner/repo" from a repository URL, for display.
func repoNameFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	path := strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
	if strings.Count(path, "/") != 1 {
		return ""
	}
	return path
}

// enhanceInput adapts a Finding to the pkg/enhance issue shape (the public
// seam predates CRDs; repo/title/body are what enhancers key on).
func enhanceInput(fnd *v1alpha1.Finding) enhance.Issue {
	issue := enhance.Issue{
		Title: fnd.Spec.Title,
		Body:  fnd.Spec.Description,
	}
	if fnd.Spec.Repository != nil {
		if owner, name, ok := splitRepo(fnd.Spec.Repository.Name); ok {
			issue.Repo = source.Repo{Owner: owner, Name: name}
		}
	}
	if cr := fnd.Spec.CloudResource; cr != nil {
		issue.CloudResource = &source.CloudResource{
			Provider:    string(cr.Provider),
			Name:        cr.Name,
			Type:        cr.Type,
			Project:     cr.Project,
			Location:    cr.Location,
			DisplayName: cr.DisplayName,
		}
	}
	if fnd.Status.Tracking != nil {
		issue.Number = int(fnd.Status.Tracking.IssueNumber)
	}
	return issue
}

// openedOnly filters watch events down to Findings awaiting enhancement.
func openedOnly() predicate.Predicate {
	awaiting := func(obj client.Object) bool {
		f, ok := obj.(*v1alpha1.Finding)
		return ok && f.Status.Phase == v1alpha1.PhaseOpened
	}
	return predicate.Funcs{
		CreateFunc:  func(e event.CreateEvent) bool { return awaiting(e.Object) },
		UpdateFunc:  func(e event.UpdateEvent) bool { return awaiting(e.ObjectNew) },
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(e event.GenericEvent) bool { return awaiting(e.Object) },
	}
}

// SetupWithManager wires the reconciler.
func (r *FindingReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Finding{}, builder.WithPredicates(openedOnly())).
		Named("finding-enhance").
		Complete(r)
}

func (r *FindingReconciler) now() time.Time {
	if r.Now == nil {
		return time.Now()
	}
	return r.Now()
}

func (r *FindingReconciler) retryAfter() time.Duration {
	if r.RetryAfter <= 0 {
		return defaultRetryAfter
	}
	return r.RetryAfter
}

func (r *FindingReconciler) log() *slog.Logger {
	if r.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return r.Log
}

// splitRepo splits "owner/name".
func splitRepo(full string) (owner, name string, ok bool) {
	for i := range full {
		if full[i] == '/' {
			return full[:i], full[i+1:], full[:i] != "" && full[i+1:] != ""
		}
	}
	return "", "", false
}

// truncate caps s at limit bytes on a rune boundary (the API server rejects
// invalid UTF-8).
func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := s[:limit]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}
