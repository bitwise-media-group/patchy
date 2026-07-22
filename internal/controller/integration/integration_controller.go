// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package integration

import (
	"context"
	"log/slog"
	"time"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
)

// defaultRevalidate paces credential revalidation when spec.interval is
// unset.
const defaultRevalidate = 10 * time.Minute

// IntegrationReconciler validates Integration credentials and maintains the
// Ready condition and receiver path.
type IntegrationReconciler struct {
	client.Client
	// Creds reads and validates the Integration's secret.
	Creds *Creds
	// Log receives reconcile diagnostics; nil discards.
	Log *slog.Logger
	// Now tells time; nil means time.Now (tests override).
	Now func() time.Time
}

// Reconcile implements the Integration control loop.
func (r *IntegrationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var integ v1alpha1.Integration
	if err := r.Get(ctx, req.NamespacedName, &integ); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !integ.DeletionTimestamp.IsZero() || integ.Spec.Suspend {
		return ctrl.Result{}, nil
	}

	cond := metav1.Condition{
		Type:               v1alpha1.ConditionReady,
		Status:             metav1.ConditionTrue,
		Reason:             "CredentialValid",
		ObservedGeneration: integ.Generation,
	}
	if err := r.Creds.Validate(ctx, &integ); err != nil {
		cond.Status = metav1.ConditionFalse
		cond.Reason = "CredentialInvalid"
		cond.Message = err.Error()
		r.log().LogAttrs(ctx, slog.LevelWarn, "integration credential invalid",
			slog.String("integration", integ.Name), slog.Any("error", err))
	}
	meta.SetStatusCondition(&integ.Status.Conditions, cond)
	integ.Status.WebhookPath = "/" + string(integ.Spec.Provider) + "/webhooks"
	integ.Status.ObservedGeneration = integ.Generation
	if cond.Status == metav1.ConditionTrue {
		if rep := pendingReplay(&integ); rep != nil {
			st, scanned := r.sweepDeliveries(ctx, &integ, r.now(), true)
			if scanned {
				st.ReplayedAt = &rep.At
			}
			integ.Status.Redelivery = st
		} else if redeliveryEnabled(&integ) {
			st, _ := r.sweepDeliveries(ctx, &integ, r.now(), false)
			// The standing sweep must not forget which replay was handled.
			if prev := integ.Status.Redelivery; prev != nil {
				st.ReplayedAt = prev.ReplayedAt
			}
			integ.Status.Redelivery = st
		}
	}
	if err := r.Status().Update(ctx, &integ); err != nil {
		if kerrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}

	interval := integ.Spec.Interval.Duration
	if interval <= 0 {
		interval = defaultRevalidate
	}
	return ctrl.Result{RequeueAfter: interval}, nil
}

// SetupWithManager wires the reconciler.
func (r *IntegrationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Integration{}).
		Named("integration").
		Complete(r)
}

func (r *IntegrationReconciler) log() *slog.Logger {
	if r.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return r.Log
}

func (r *IntegrationReconciler) now() time.Time {
	if r.Now == nil {
		return time.Now()
	}
	return r.Now()
}
