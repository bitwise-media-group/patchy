// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package evaluation

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/bitwise-media-group/patchy/api/v1alpha1"
)

// UnitName is the deterministic child name for one unit index.
func UnitName(eval string, index int) string {
	return fmt.Sprintf("%s-u%03d", eval, index)
}

// GateReconciler expands a new Evaluation into its EvaluationUnit children.
// Creation is idempotent (deterministic names, AlreadyExists adopted), so a
// crashed expansion resumes where it stopped. Units whose harness preference
// list has no overlap with the enabled fleet are settled immediately as
// Failed/HarnessUnavailable — deterministic, never a silent drop.
type GateReconciler struct {
	client.Client
	Namespace string
	// EnabledHarnesses is the evolve-runner fleet resolved at startup.
	EnabledHarnesses []string
	Now              func() time.Time
	Log              *slog.Logger
}

func (r *GateReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Reconcile creates missing children and moves the parent to Running.
func (r *GateReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var eval v1alpha1.Evaluation
	if err := r.Get(ctx, req.NamespacedName, &eval); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !eval.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	if eval.Status.Phase != "" && eval.Status.Phase != v1alpha1.EvaluationPending {
		return ctrl.Result{}, nil
	}

	for i := range eval.Spec.Units {
		if err := r.ensureUnit(ctx, &eval, i); err != nil {
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var cur v1alpha1.Evaluation
		if err := r.Get(ctx, req.NamespacedName, &cur); err != nil {
			return client.IgnoreNotFound(err)
		}
		if cur.Status.Phase != "" && cur.Status.Phase != v1alpha1.EvaluationPending {
			return nil
		}
		cur.Status.Phase = v1alpha1.EvaluationRunning
		cur.Status.Units = int32(len(cur.Spec.Units))
		if cur.Status.StartedAt == nil {
			now := metav1.NewTime(r.now())
			cur.Status.StartedAt = &now
		}
		cur.Status.ObservedGeneration = cur.Generation
		meta.SetStatusCondition(&cur.Status.Conditions, metav1.Condition{
			Type:               v1alpha1.ConditionUnitsCreated,
			Status:             metav1.ConditionTrue,
			Reason:             "UnitsCreated",
			Message:            fmt.Sprintf("%d units created", len(cur.Spec.Units)),
			ObservedGeneration: cur.Generation,
		})
		return r.Status().Update(ctx, &cur)
	})
}

// ensureUnit creates one child (idempotent) and settles it immediately when
// no harness in its preference list is enabled.
func (r *GateReconciler) ensureUnit(ctx context.Context, eval *v1alpha1.Evaluation, index int) error {
	plan := eval.Spec.Units[index]
	name := UnitName(eval.Name, index)
	unit := &v1alpha1.EvaluationUnit{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: eval.Namespace,
			Labels: map[string]string{
				v1alpha1.LabelEvaluation: eval.Name,
				v1alpha1.LabelUnitIndex:  fmt.Sprintf("%d", index),
			},
			Finalizers: []string{v1alpha1.FinalizerJobs},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(eval, v1alpha1.GroupVersion.WithKind("Evaluation")),
			},
		},
		Spec: v1alpha1.EvaluationUnitSpec{
			EvaluationRef: v1alpha1.ObjectReference{Name: eval.Name, UID: eval.UID},
			Index:         int32(index),
			Unit:          plan,
		},
	}
	if err := r.Create(ctx, unit); err != nil && !kerrors.IsAlreadyExists(err) {
		return fmt.Errorf("create evaluation unit %s: %w", name, err)
	}

	if resolveHarness(plan.Harnesses, r.EnabledHarnesses) == "" {
		return r.settleUnavailable(ctx, eval.Namespace, name, plan)
	}
	return nil
}

// settleUnavailable marks a just-created unit Failed/HarnessUnavailable.
func (r *GateReconciler) settleUnavailable(ctx context.Context, namespace, name string, plan v1alpha1.UnitPlan) error {
	prefs := make([]string, 0, len(plan.Harnesses))
	for _, h := range plan.Harnesses {
		prefs = append(prefs, h.Harness)
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var unit v1alpha1.EvaluationUnit
		if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &unit); err != nil {
			return client.IgnoreNotFound(err)
		}
		if terminalRun(unit.Status.Phase) {
			return nil
		}
		now := metav1.NewTime(r.now())
		unit.Status.Phase = v1alpha1.RunFailed
		unit.Status.Reason = v1alpha1.UnitHarnessUnavailable
		unit.Status.Detail = fmt.Sprintf("no enabled harness among %v (fleet: %v)", prefs, r.EnabledHarnesses)
		unit.Status.FinishedAt = &now
		unit.Status.ObservedGeneration = unit.Generation
		meta.SetStatusCondition(&unit.Status.Conditions, metav1.Condition{
			Type:               v1alpha1.ConditionComplete,
			Status:             metav1.ConditionFalse,
			Reason:             string(v1alpha1.UnitHarnessUnavailable),
			Message:            unit.Status.Detail,
			ObservedGeneration: unit.Generation,
		})
		return r.Status().Update(ctx, &unit)
	})
}

// resolveHarness returns the first preferred harness that is enabled, or "".
func resolveHarness(prefs []v1alpha1.HarnessOption, enabled []string) string {
	for _, p := range prefs {
		if slices.Contains(enabled, p.Harness) {
			return p.Harness
		}
	}
	return ""
}

// terminalRun reports whether a unit phase is settled.
func terminalRun(p v1alpha1.RunPhase) bool {
	return p == v1alpha1.RunComplete || p == v1alpha1.RunFailed
}

// SetupWithManager registers the gate.
func (r *GateReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Evaluation{}).
		Named("evaluation-gate").
		Complete(r)
}
