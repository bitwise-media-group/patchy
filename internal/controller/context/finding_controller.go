// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package context

import (
	"context"
	"log/slog"
	"slices"
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

// FindingReconciler runs the enhancer chain over freshly opened Findings —
// the CRD-native context-controller. It writes only Finding status
// (enrichments, owners, the ContextEnhanced condition, and the
// Opened→Enhanced edge); it holds no tracking-system credential — comments
// are integration-controller projection work.
type FindingReconciler struct {
	client.Client
	// Enhancers run in order; each contributes at most one enrichment.
	Enhancers []enhance.Enhancer
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

	issue := enhanceInput(&fnd)
	var enrichments []v1alpha1.Enrichment
	var owners []string
	for _, e := range r.Enhancers {
		enr, err := e.Enhance(ctx, issue)
		if err != nil {
			// One broken enhancer must not wedge the pipeline: log and move on.
			r.log().LogAttrs(ctx, slog.LevelWarn, "enhancer failed",
				slog.String("enhancer", e.ID()),
				slog.String("finding", fnd.Name),
				slog.Any("error", err))
			continue
		}
		if enr == nil {
			continue
		}
		if len(enrichments) < 8 {
			enrichments = append(enrichments, v1alpha1.Enrichment{
				Enhancer:  e.ID(),
				Owners:    enr.Owners,
				Markdown:  truncate(enr.CommentMarkdown, maxEnrichmentMarkdown),
				AppliedAt: metav1.NewTime(r.now()),
			})
		}
		for _, o := range enr.Owners {
			if !slices.Contains(owners, o) {
				owners = append(owners, o)
			}
		}
	}

	fnd.Status.Enrichments = enrichments
	fnd.Status.Owners = owners
	meta.SetStatusCondition(&fnd.Status.Conditions, metav1.Condition{
		Type:               v1alpha1.ConditionContextEnhanced,
		Status:             metav1.ConditionTrue,
		Reason:             "EnhancerChainComplete",
		ObservedGeneration: fnd.Generation,
	})
	if err := v1alpha1.SetPhase(&fnd, v1alpha1.PhaseEnhanced, r.now()); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.Status().Update(ctx, &fnd); err != nil {
		if kerrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}
	r.log().LogAttrs(ctx, slog.LevelInfo, "finding enhanced",
		slog.String("finding", fnd.Name),
		slog.Int("enrichments", len(enrichments)),
		slog.Int("owners", len(owners)))
	return ctrl.Result{}, nil
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
