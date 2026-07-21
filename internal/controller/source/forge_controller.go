// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package source

import (
	"context"
	"log/slog"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/forge"
)

// defaultRevalidate paces credential revalidation when spec.interval is
// unset.
const defaultRevalidate = 10 * time.Minute

// ForgeReconciler validates Forge credentials and maintains the Ready
// condition.
type ForgeReconciler struct {
	client.Client
	// Forges mints and validates credentials (shared with the Repository
	// reconciler).
	Forges *forge.Store
	// Log receives reconcile diagnostics; nil discards.
	Log *slog.Logger
}

// Reconcile implements the Forge control loop.
func (r *ForgeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var f v1alpha1.Forge
	if err := r.Get(ctx, req.NamespacedName, &f); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !f.DeletionTimestamp.IsZero() || f.Spec.Suspend {
		return ctrl.Result{}, nil
	}

	cond := metav1.Condition{
		Type:               v1alpha1.ConditionReady,
		Status:             metav1.ConditionTrue,
		Reason:             "CredentialValid",
		ObservedGeneration: f.Generation,
	}
	if err := r.Forges.Validate(ctx, &f); err != nil {
		cond.Status = metav1.ConditionFalse
		cond.Reason = "CredentialInvalid"
		cond.Message = err.Error()
		r.log().LogAttrs(ctx, slog.LevelWarn, "forge credential invalid",
			slog.String("forge", f.Name), slog.Any("error", err))
	}
	meta.SetStatusCondition(&f.Status.Conditions, cond)
	f.Status.ObservedGeneration = f.Generation
	if err := r.Status().Update(ctx, &f); err != nil {
		if errors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}

	interval := f.Spec.Interval.Duration
	if interval <= 0 {
		interval = defaultRevalidate
	}
	return ctrl.Result{RequeueAfter: interval}, nil
}

// SetupWithManager wires the reconciler.
func (r *ForgeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Forge{}).
		Named("forge").
		Complete(r)
}

func (r *ForgeReconciler) log() *slog.Logger {
	if r.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return r.Log
}
