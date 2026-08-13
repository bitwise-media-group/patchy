// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package investigation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/forge"
)

// FindingPhaseIndex is the field index over status.phase, registered by the
// gate: its Forge watch re-gates only Enhanced findings, and without the
// index that selection is a full informer-store scan per Forge event.
const FindingPhaseIndex = "status.phase"

// FindingPhaseIndexer extracts the index value; findings with no phase yet
// are left out. Exported so fake-client tests register the same recipe.
func FindingPhaseIndexer(obj client.Object) []string {
	f, ok := obj.(*v1alpha1.Finding)
	if !ok || f.Status.Phase == "" {
		return nil
	}
	return []string{string(f.Status.Phase)}
}

// GateReconciler admits Enhanced findings into investigation: gates on the
// accumulation window and minimum age, resolves the Forge, materializes the
// Repository artifact, and creates the Investigation child (the lease).
type GateReconciler struct {
	client.Client
	// Forges resolves the covering Forge (read-only here; tokens are minted
	// by source-controller for the artifact and remediation-controller for
	// pushes).
	Forges *forge.Store
	// Namespace the findings live in.
	Namespace string
	// MinAge a finding must reach before investigation picks it up.
	MinAge time.Duration
	// Concurrency is the number of findings gated in parallel; <=1 means
	// serial. Safe to raise: child names are deterministic per attempt, the
	// create tolerates AlreadyExists, and the phase advance runs under
	// conflict retry — two racing reconciles converge on one lease.
	Concurrency int
	// Parameters bound the analysis stage (model/turns/budget), from flags.
	Parameters v1alpha1.AgentParameters
	// Now is the clock seam; nil means time.Now.
	Now func() time.Time
	// Log receives diagnostics; nil discards.
	Log *slog.Logger
}

// Reconcile gates one finding.
func (r *GateReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var fnd v1alpha1.Finding
	if err := r.Get(ctx, req.NamespacedName, &fnd); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !fnd.DeletionTimestamp.IsZero() || fnd.Spec.Suspend {
		return ctrl.Result{}, nil
	}
	// A failed investigation a human asked to retry recovers to Enhanced
	// (edge Failed→Enhanced); the next reconcile runs the normal gate.
	if fnd.Status.Phase == v1alpha1.PhaseFailed {
		if v1alpha1.RetryRequested(&fnd) && v1alpha1.RetryTarget(&fnd) == v1alpha1.PhaseEnhanced {
			return ctrl.Result{}, r.retryRevert(ctx, &fnd)
		}
		return ctrl.Result{}, nil
	}
	if fnd.Status.Phase != v1alpha1.PhaseEnhanced {
		return ctrl.Result{}, nil
	}

	// Gates 1 and 2 are waiting stages; an expedited finding skips both.
	if fnd.Spec.Expedite == nil {
		// Gate 1: the accumulation window must be closed.
		if !meta.IsStatusConditionTrue(fnd.Status.Conditions, v1alpha1.ConditionAccumulationComplete) {
			return ctrl.Result{}, nil // the window condition flip re-queues us
		}
		// Gate 2: minimum age.
		if first := fnd.Status.FirstObservedAt; first != nil {
			if wait := first.Add(r.MinAge).Sub(r.now()); wait > 0 {
				return ctrl.Result{RequeueAfter: wait}, nil
			}
		}
	}

	// Repo-less findings can never be investigated in a workspace; hand off.
	if fnd.Spec.Repository == nil {
		return ctrl.Result{}, r.park(ctx, &fnd, v1alpha1.ReasonNoRepository,
			"finding has no identifiable repository", true)
	}

	res, err := r.Forges.Resolve(ctx, fnd.Namespace, fnd.Spec.Repository.URL)
	switch {
	case errors.Is(err, forge.ErrNoMatch):
		// Recoverable by operator action: stay Enhanced; the Forge watch
		// re-gates parked findings automatically.
		return ctrl.Result{}, r.park(ctx, &fnd, v1alpha1.ReasonNoForgeMatch, err.Error(), false)
	case errors.Is(err, forge.ErrAmbiguous):
		return ctrl.Result{}, r.park(ctx, &fnd, v1alpha1.ReasonAmbiguous, err.Error(), false)
	case err != nil:
		return ctrl.Result{}, err
	}

	repoReady, err := r.ensureRepository(ctx, &fnd)
	if err != nil || !repoReady {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, r.openInvestigation(ctx, &fnd, res)
}

// ensureRepository creates the Finding's Repository artifact request and
// reports whether its artifact is ready.
func (r *GateReconciler) ensureRepository(ctx context.Context, fnd *v1alpha1.Finding) (bool, error) {
	name := fnd.Name + "-src"
	var repo v1alpha1.Repository
	err := r.Get(ctx, types.NamespacedName{Namespace: fnd.Namespace, Name: name}, &repo)
	if kerrors.IsNotFound(err) {
		repo = v1alpha1.Repository{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: fnd.Namespace,
				Labels:    map[string]string{v1alpha1.LabelFinding: fnd.Name},
				OwnerReferences: []metav1.OwnerReference{
					*metav1.NewControllerRef(fnd, v1alpha1.GroupVersion.WithKind("Finding")),
				},
			},
			Spec: v1alpha1.RepositorySpec{
				URL: fnd.Spec.Repository.URL,
				Ref: v1alpha1.RepositoryRef{Branch: fnd.Spec.Repository.DefaultBranch},
			},
		}
		if err := r.Create(ctx, &repo); err != nil && !kerrors.IsAlreadyExists(err) {
			return false, fmt.Errorf("create repository %s: %w", name, err)
		}
		return false, nil // the Repository watch re-queues us on readiness
	}
	if err != nil {
		return false, err
	}
	if meta.IsStatusConditionTrue(repo.Status.Conditions, v1alpha1.ConditionStalled) {
		// Oversized artifact: a human must intervene.
		return false, r.park(ctx, fnd, "ArtifactStalled",
			"repository artifact exceeds the size cap", true)
	}
	ready := meta.IsStatusConditionTrue(repo.Status.Conditions, v1alpha1.ConditionReady)
	return ready, nil
}

// openInvestigation creates the next Investigation attempt and moves the
// finding to Investigating; the deterministic child name makes the create
// the lease.
func (r *GateReconciler) openInvestigation(ctx context.Context, fnd *v1alpha1.Finding, res *forge.Resolved) error {
	attempt := fnd.Status.Attempts.Investigation + 1
	name := fmt.Sprintf("%s-inv-%d", fnd.Name, attempt)
	inv := &v1alpha1.Investigation{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: fnd.Namespace,
			Labels: map[string]string{
				v1alpha1.LabelFinding:  fnd.Name,
				v1alpha1.LabelAttempt:  fmt.Sprintf("%d", attempt),
				v1alpha1.LabelSeverity: string(fnd.Spec.Severity),
			},
			Annotations: map[string]string{
				v1alpha1.AnnotationRepo: fnd.Spec.Repository.Name,
			},
			Finalizers: []string{
				v1alpha1.FinalizerJobs,
				v1alpha1.FinalizerRollupTotal,
				v1alpha1.FinalizerRollupRepository,
				v1alpha1.FinalizerRollupHarness,
				v1alpha1.FinalizerRollupModel,
			},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(fnd, v1alpha1.GroupVersion.WithKind("Finding")),
			},
		},
		Spec: v1alpha1.InvestigationSpec{
			FindingRef:    v1alpha1.ObjectReference{Name: fnd.Name, UID: fnd.UID},
			Attempt:       attempt,
			RepositoryRef: &v1alpha1.LocalObjectReference{Name: fnd.Name + "-src"},
			Parameters:    r.Parameters,
		},
	}
	if err := r.Create(ctx, inv); err != nil && !kerrors.IsAlreadyExists(err) {
		return fmt.Errorf("create investigation %s: %w", name, err)
	}

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var cur v1alpha1.Finding
		if err := r.Get(ctx, client.ObjectKeyFromObject(fnd), &cur); err != nil {
			return client.IgnoreNotFound(err)
		}
		if cur.Status.Phase != v1alpha1.PhaseEnhanced {
			return nil // raced; the lease holder already advanced it
		}
		if err := v1alpha1.SetPhase(&cur, v1alpha1.PhaseInvestigating, r.now()); err != nil {
			return err
		}
		cur.Status.Attempts.Investigation = attempt
		cur.Status.ActiveRun = &v1alpha1.ActiveRun{Kind: v1alpha1.RunKindInvestigation, Name: name}
		cur.Status.Forge = &v1alpha1.LocalObjectReference{Name: res.Forge.Name}
		meta.SetStatusCondition(&cur.Status.Conditions, metav1.Condition{
			Type:               v1alpha1.ConditionForgeResolved,
			Status:             metav1.ConditionTrue,
			Reason:             "Resolved",
			Message:            res.Forge.Name,
			ObservedGeneration: cur.Generation,
		})
		return r.Status().Update(ctx, &cur)
	})
	if err != nil {
		return err
	}
	r.log().LogAttrs(ctx, slog.LevelInfo, "investigation opened",
		slog.String("finding", fnd.Name), slog.String("investigation", name))
	return nil
}

// retryRevert consumes a human retry of a failed investigation: the finding
// recovers to Enhanced and the gate re-admits it on the next reconcile,
// opening the next attempt. Consumption is implicit — the transition clears
// status.completedAt, so RetryRequested turns false without a spec write.
func (r *GateReconciler) retryRevert(ctx context.Context, fnd *v1alpha1.Finding) error {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var cur v1alpha1.Finding
		if err := r.Get(ctx, client.ObjectKeyFromObject(fnd), &cur); err != nil {
			return client.IgnoreNotFound(err)
		}
		if cur.Status.Phase != v1alpha1.PhaseFailed || !v1alpha1.RetryRequested(&cur) ||
			v1alpha1.RetryTarget(&cur) != v1alpha1.PhaseEnhanced {
			return nil
		}
		if err := v1alpha1.SetPhase(&cur, v1alpha1.PhaseEnhanced, r.now()); err != nil {
			return err
		}
		meta.SetStatusCondition(&cur.Status.Conditions, metav1.Condition{
			Type:               v1alpha1.ConditionRetried,
			Status:             metav1.ConditionTrue,
			Reason:             "RetryRequested",
			Message:            cur.Spec.Retry.By,
			ObservedGeneration: cur.Generation,
		})
		return r.Status().Update(ctx, &cur)
	})
	if err == nil {
		r.log().LogAttrs(ctx, slog.LevelInfo, "failed finding recovered for retry",
			slog.String("finding", fnd.Name), slog.String("to", string(v1alpha1.PhaseEnhanced)))
	}
	return err
}

// park records why the finding cannot proceed. Terminal parks (no
// repository, stalled artifact) hand the finding to humans; recoverable
// parks leave it Enhanced for the Forge watch to revive.
func (r *GateReconciler) park(ctx context.Context, fnd *v1alpha1.Finding, reason, message string, handOff bool) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var cur v1alpha1.Finding
		if err := r.Get(ctx, client.ObjectKeyFromObject(fnd), &cur); err != nil {
			return client.IgnoreNotFound(err)
		}
		meta.SetStatusCondition(&cur.Status.Conditions, metav1.Condition{
			Type:               v1alpha1.ConditionForgeResolved,
			Status:             metav1.ConditionFalse,
			Reason:             reason,
			Message:            message,
			ObservedGeneration: cur.Generation,
		})
		if handOff && cur.Status.Phase == v1alpha1.PhaseEnhanced {
			if err := v1alpha1.SetPhase(&cur, v1alpha1.PhaseHandedOff, r.now()); err != nil {
				return err
			}
			cur.Status.LastFailureReason = message
		}
		return r.Status().Update(ctx, &cur)
	})
}

// SetupWithManager wires the gate: Findings, plus Forge and Repository
// watches that re-queue affected findings (an operator adding a Forge must
// revive parked findings without human action; a Repository turning Ready
// resumes its finding).
//
// The Forge watch is the fan-out bomb of this controller — one Forge event
// re-queues every Enhanced finding — so it is bounded twice: the phase field
// index turns the selection from a full scan into a lookup, and the
// generation predicate drops the status-only Forge updates (the source
// controller's validation writes) that cannot change resolution. Forge
// resolution reads spec alone, so only spec changes (and create/delete) can
// revive a parked finding.
func (r *GateReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(), &v1alpha1.Finding{}, FindingPhaseIndex, FindingPhaseIndexer); err != nil {
		return fmt.Errorf("index %s: %w", FindingPhaseIndex, err)
	}
	mapForge := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []ctrl.Request {
		var findings v1alpha1.FindingList
		if err := mgr.GetClient().List(ctx, &findings, client.InNamespace(obj.GetNamespace()),
			client.MatchingFields{FindingPhaseIndex: string(v1alpha1.PhaseEnhanced)}); err != nil {
			return nil
		}
		out := make([]ctrl.Request, 0, len(findings.Items))
		for i := range findings.Items {
			out = append(out, ctrl.Request{NamespacedName: types.NamespacedName{
				Namespace: findings.Items[i].Namespace, Name: findings.Items[i].Name,
			}})
		}
		return out
	})
	mapRepo := handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []ctrl.Request {
		name := obj.GetLabels()[v1alpha1.LabelFinding]
		if name == "" {
			return nil
		}
		return []ctrl.Request{{NamespacedName: types.NamespacedName{Namespace: obj.GetNamespace(), Name: name}}}
	})
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Finding{}).
		Watches(&v1alpha1.Forge{}, mapForge,
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&v1alpha1.Repository{}, mapRepo).
		WithOptions(controller.Options{MaxConcurrentReconciles: max(1, r.Concurrency)}).
		Named("investigation-gate").
		Complete(r)
}

func (r *GateReconciler) now() time.Time {
	if r.Now == nil {
		return time.Now()
	}
	return r.Now()
}

func (r *GateReconciler) log() *slog.Logger {
	if r.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return r.Log
}
