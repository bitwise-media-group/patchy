// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package remediation

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/priority"
)

// SpawnerReconciler is the queue-admission writer: it turns approvals and
// revivals into Queued findings (edges 10 and 18) and materializes one
// Pending Remediation per attempt for every Queued finding, stamped with its
// scheduling priority.
type SpawnerReconciler struct {
	client.Client
	// Namespace the CRs live in.
	Namespace string
	// Weights tune the scheduling-priority computation.
	Weights priority.Weights
	// Now is the clock seam; nil means time.Now.
	Now func() time.Time
	// Log receives diagnostics; nil discards.
	Log *slog.Logger
}

// Reconcile advances one finding through queue admission.
func (r *SpawnerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var fnd v1alpha1.Finding
	if err := r.Get(ctx, req.NamespacedName, &fnd); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !fnd.DeletionTimestamp.IsZero() || fnd.Spec.Suspend {
		return ctrl.Result{}, nil
	}

	switch fnd.Status.Phase {
	case v1alpha1.PhaseAwaitingApproval:
		if fnd.Spec.Approval == nil {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, r.admit(ctx, &fnd, false)
	case v1alpha1.PhaseHandedOff:
		if !revivable(&fnd) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, r.admit(ctx, &fnd, true)
	case v1alpha1.PhaseQueued:
		return ctrl.Result{}, r.spawn(ctx, &fnd)
	default:
		return ctrl.Result{}, nil
	}
}

// revivable reports whether a handed-off finding carries an approval newer
// than its completion — the human asked patchy to take it back.
func revivable(fnd *v1alpha1.Finding) bool {
	ap := fnd.Spec.Approval
	if ap == nil || fnd.Spec.Repository == nil || fnd.Status.Investigation == nil {
		return false
	}
	done := fnd.Status.CompletedAt
	return done == nil || ap.At.After(done.Time)
}

// admit moves the finding into the queue (edge 10 or 18) with the Approved
// condition.
func (r *SpawnerReconciler) admit(ctx context.Context, fnd *v1alpha1.Finding, revival bool) error {
	from := fnd.Status.Phase
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var cur v1alpha1.Finding
		if err := r.Get(ctx, client.ObjectKeyFromObject(fnd), &cur); err != nil {
			return client.IgnoreNotFound(err)
		}
		if cur.Status.Phase != from {
			return nil
		}
		if err := v1alpha1.SetPhase(&cur, v1alpha1.PhaseQueued, r.now()); err != nil {
			return err
		}
		meta.SetStatusCondition(&cur.Status.Conditions, metav1.Condition{
			Type:               v1alpha1.ConditionApproved,
			Status:             metav1.ConditionTrue,
			Reason:             "ApprovalReceived",
			Message:            cur.Spec.Approval.By,
			ObservedGeneration: cur.Generation,
		})
		return r.Status().Update(ctx, &cur)
	})
	if err == nil {
		r.log().LogAttrs(ctx, slog.LevelInfo, "finding admitted to remediation queue",
			slog.String("finding", fnd.Name), slog.Bool("revival", revival))
	}
	return err
}

// spawn ensures the Queued finding has its Pending Remediation child.
func (r *SpawnerReconciler) spawn(ctx context.Context, fnd *v1alpha1.Finding) error {
	inv := fnd.Status.Investigation
	if inv == nil || fnd.Spec.Repository == nil {
		return nil // nothing to execute; the phase table prevents this in practice
	}
	// Spawn the next attempt only when the latest one is settled: a Pending
	// child awaiting its scheduler grant must not breed a sibling.
	if latest := fnd.Status.Attempts.Remediation; latest > 0 {
		var cur v1alpha1.Remediation
		latestName := fmt.Sprintf("%s-rem-%d", fnd.Name, latest)
		err := r.Get(ctx, types.NamespacedName{Namespace: fnd.Namespace, Name: latestName}, &cur)
		switch {
		case err == nil && cur.Status.Phase != v1alpha1.RunFailed:
			return nil // in flight (Pending/Running) or already succeeded
		case err != nil && !kerrors.IsNotFound(err):
			return err
		}
	}
	attempt := fnd.Status.Attempts.Remediation + 1
	name := fmt.Sprintf("%s-rem-%d", fnd.Name, attempt)

	// The suggested stage-2 parameters live on the Investigation child.
	var params v1alpha1.AgentParameters
	var invChild v1alpha1.Investigation
	invKey := types.NamespacedName{Namespace: fnd.Namespace, Name: inv.Name}
	if err := r.Get(ctx, invKey, &invChild); err == nil && invChild.Status.RemediationParameters != nil {
		params = *invChild.Status.RemediationParameters
	}

	score := priority.Score(fnd.Spec.Severity, inv.Exploitability, inv.Likelihood, inv.Impact, r.Weights)
	rem := &v1alpha1.Remediation{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: fnd.Namespace,
			Labels: map[string]string{
				v1alpha1.LabelFinding: fnd.Name,
				v1alpha1.LabelAttempt: fmt.Sprintf("%d", attempt),
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
		Spec: v1alpha1.RemediationSpec{
			FindingRef:       v1alpha1.ObjectReference{Name: fnd.Name, UID: fnd.UID},
			InvestigationRef: v1alpha1.ObjectReference{Name: inv.Name},
			RepositoryRef:    v1alpha1.LocalObjectReference{Name: fnd.Name + "-src"},
			Attempt:          attempt,
			Priority:         score,
			Parameters:       params,
			ApprovedBy:       approvedBy(fnd),
			Revival:          fnd.Spec.Approval != nil && attempt > 1,
		},
	}
	if err := r.Create(ctx, rem); err != nil && !kerrors.IsAlreadyExists(err) {
		return fmt.Errorf("create remediation %s: %w", name, err)
	}

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var cur v1alpha1.Finding
		if err := r.Get(ctx, client.ObjectKeyFromObject(fnd), &cur); err != nil {
			return client.IgnoreNotFound(err)
		}
		if cur.Status.Attempts.Remediation >= attempt {
			return nil
		}
		cur.Status.Attempts.Remediation = attempt
		return r.Status().Update(ctx, &cur)
	})
}

// approvedBy extracts the approver, empty when none.
func approvedBy(fnd *v1alpha1.Finding) string {
	if fnd.Spec.Approval == nil {
		return ""
	}
	return fnd.Spec.Approval.By
}

// SetupWithManager wires the spawner.
func (r *SpawnerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Finding{}).
		Named("remediation-spawner").
		Complete(r)
}

func (r *SpawnerReconciler) now() time.Time {
	if r.Now == nil {
		return time.Now()
	}
	return r.Now()
}

func (r *SpawnerReconciler) log() *slog.Logger {
	if r.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return r.Log
}
