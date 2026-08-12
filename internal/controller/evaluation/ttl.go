// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package evaluation

import (
	"context"
	"log/slog"
	"time"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/bitwise-media-group/patchy/api/v1alpha1"
)

// DefaultTTL retains a finished Evaluation for three days: long enough for
// the submitting client to reconnect and reassemble results after a weekend,
// short enough that etcd never accumulates finished batches.
const DefaultTTL = 72 * time.Hour

// TTLReconciler deletes finished Evaluations after their retention; deletion
// cascades to units, Jobs (via the unit finalizer), and results ConfigMaps.
// Expiry is deletion, not a phase.
type TTLReconciler struct {
	client.Client
	Namespace string
	// TTL is the default retention. 0 keeps forever; <0 means DefaultTTL.
	// spec.ttlSecondsAfterFinished overrides per evaluation.
	TTL time.Duration
	Now func() time.Time
	Log *slog.Logger
}

func (r *TTLReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Reconcile enforces the TTL on one Evaluation.
func (r *TTLReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var eval v1alpha1.Evaluation
	if err := r.Get(ctx, req.NamespacedName, &eval); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !eval.DeletionTimestamp.IsZero() || !terminalEvaluation(eval.Status.Phase) || eval.Status.CompletedAt == nil {
		return ctrl.Result{}, nil
	}

	ttl := r.TTL
	if ttl < 0 {
		ttl = DefaultTTL
	}
	if o := eval.Spec.TTLSecondsAfterFinished; o != nil {
		ttl = time.Duration(*o) * time.Second
	}
	if ttl == 0 {
		return ctrl.Result{}, nil
	}

	expiry := eval.Status.CompletedAt.Add(ttl)
	if wait := expiry.Sub(r.now()); wait > 0 {
		return ctrl.Result{RequeueAfter: wait}, nil
	}
	if err := r.Delete(ctx, &eval); err != nil && !kerrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	if r.Log != nil {
		r.Log.LogAttrs(ctx, slog.LevelInfo, "evaluation expired",
			slog.String("evaluation", eval.Name))
	}
	return ctrl.Result{}, nil
}

// SetupWithManager registers the TTL loop.
func (r *TTLReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Evaluation{}).
		Named("evaluation-ttl").
		Complete(reconcile.Func(r.Reconcile))
}
