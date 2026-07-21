// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package rollupctrl maintains the all-time FindingRollup statistics and the
// TTL deletion of completed findings — causally ordered: nothing is deleted
// before its spend is aggregated into every scope.
//
// Exactly-once accounting: every Investigation/Remediation child contributes
// one immutable stage delta per scope (ledger key i:<uid> / r:<uid>); every
// Finding contributes terminal-phase counts to the total and repository
// scopes (ledger key f:<uid>:<seq>, with revival corrections at seq+1). A
// per-scope condition marks aggregation on the contributing object, and the
// matching rollup finalizer is removed only when the object is deleting and
// its scope condition is true — kubectl delete can never lose un-aggregated
// spend, and remaining finalizers show exactly which scopes are owed.
package rollupctrl

import (
	"context"
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
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/stats"
)

// DefaultTTL keeps completed findings for 14 days.
const DefaultTTL = 336 * time.Hour

// Reconciler aggregates rollups and enforces the finding TTL.
type Reconciler struct {
	client.Client
	// Namespace the CRs live in.
	Namespace string
	// TTL after completedAt before a finding is deleted; 0 keeps forever.
	TTL time.Duration
	// Now is the clock seam; nil means time.Now.
	Now func() time.Time
	// Log receives diagnostics; nil discards.
	Log *slog.Logger
}

// scopeTarget pairs a rollup scope with its per-object marker condition and
// finalizer.
type scopeTarget struct {
	scope     v1alpha1.RollupScope
	condition string
	finalizer string
}

// childScopes are the four scopes a run contributes to; harness/model keys
// may be empty (a run that died without reporting) — those scopes then get a
// trivial marker so deletion is never blocked on data that cannot exist.
func childScopes(repoKey, harness, model string) []scopeTarget {
	return []scopeTarget{
		{
			scope:     v1alpha1.RollupScope{Type: v1alpha1.ScopeTotal},
			condition: v1alpha1.ConditionRolledUpTotal,
			finalizer: v1alpha1.FinalizerRollupTotal,
		},
		{
			scope:     v1alpha1.RollupScope{Type: v1alpha1.ScopeRepository, Key: repoKey},
			condition: v1alpha1.ConditionRolledUpRepository,
			finalizer: v1alpha1.FinalizerRollupRepository,
		},
		{
			scope:     v1alpha1.RollupScope{Type: v1alpha1.ScopeHarness, Key: harness},
			condition: v1alpha1.ConditionRolledUpHarness,
			finalizer: v1alpha1.FinalizerRollupHarness,
		},
		{
			scope:     v1alpha1.RollupScope{Type: v1alpha1.ScopeModel, Key: model},
			condition: v1alpha1.ConditionRolledUpModel,
			finalizer: v1alpha1.FinalizerRollupModel,
		},
	}
}

// ReconcileInvestigation aggregates one investigation run.
func (r *Reconciler) ReconcileInvestigation(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var inv v1alpha1.Investigation
	if err := r.Get(ctx, req.NamespacedName, &inv); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	succeeded := inv.Status.Phase == v1alpha1.RunComplete
	settled := succeeded || inv.Status.Phase == v1alpha1.RunFailed
	return ctrl.Result{}, r.child(ctx, &inv, childObj{
		kind: "investigation", ledger: "i:" + string(inv.UID),
		stage: inv.Status.Stage, succeeded: succeeded, settled: settled,
		conditions: &inv.Status.Conditions,
		update:     func() error { return r.Status().Update(ctx, &inv) },
	})
}

// ReconcileRemediation aggregates one remediation run.
func (r *Reconciler) ReconcileRemediation(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var rem v1alpha1.Remediation
	if err := r.Get(ctx, req.NamespacedName, &rem); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	succeeded := rem.Status.Phase == v1alpha1.RunComplete && rem.Status.Success
	settled := rem.Status.Phase == v1alpha1.RunComplete || rem.Status.Phase == v1alpha1.RunFailed
	return ctrl.Result{}, r.child(ctx, &rem, childObj{
		kind: "remediation", ledger: "r:" + string(rem.UID),
		stage: rem.Status.Stage, succeeded: succeeded, settled: settled,
		conditions: &rem.Status.Conditions,
		update:     func() error { return r.Status().Update(ctx, &rem) },
	})
}

// childObj adapts the two child kinds for aggregation.
type childObj struct {
	kind       string
	ledger     string
	stage      *v1alpha1.StageResult
	succeeded  bool
	settled    bool
	conditions *[]metav1.Condition
	update     func() error
}

// child aggregates one run into every owed scope, then releases finalizers
// on deletion.
func (r *Reconciler) child(ctx context.Context, obj client.Object, c childObj) error {
	deleting := !obj.GetDeletionTimestamp().IsZero()
	if !c.settled && !deleting {
		return nil
	}
	delta, err := stats.StageDeltaFrom(c.kind, c.stage, c.succeeded)
	if err != nil {
		r.log().LogAttrs(ctx, slog.LevelWarn, "unparseable stage cost; counted as zero",
			slog.String(c.kind, obj.GetName()), slog.Any("error", err))
	}
	repoKey := obj.GetAnnotations()[v1alpha1.AnnotationRepo]
	month := r.now().UTC().Format("2006-01")

	changed := false
	for _, target := range childScopes(repoKey, delta.Harness, delta.Model) {
		if meta.IsStatusConditionTrue(*c.conditions, target.condition) {
			continue
		}
		reason := "Aggregated"
		// A scope without a key has nothing to aggregate into: repository is
		// unknown only when the finding vanished first; harness/model are
		// unknown when the run died before reporting.
		if target.scope.Type != v1alpha1.ScopeTotal && target.scope.Key == "" {
			reason = "NoScopeKey"
		} else {
			d := delta
			if err := r.applyScope(ctx, target.scope, c.ledger, &d, nil, month); err != nil {
				return err
			}
			if target.scope.Type == v1alpha1.ScopeTotal {
				stats.RecordStage(ctx, delta, repoKey)
			}
		}
		meta.SetStatusCondition(c.conditions, metav1.Condition{
			Type:   target.condition,
			Status: metav1.ConditionTrue,
			Reason: reason,
		})
		changed = true
	}
	if changed {
		if err := c.update(); err != nil {
			if kerrors.IsConflict(err) {
				return nil // re-queued; conditions re-derive
			}
			return err
		}
	}
	if deleting {
		return r.releaseFinalizers(ctx, obj, *c.conditions)
	}
	return nil
}

// countedMarker encodes what a finding's scope condition already counted.
func countedMarker(phase string, seq int) string {
	return fmt.Sprintf("phase=%s;seq=%d", phase, seq)
}

// parseCounted decodes a counted marker; zero seq means never counted.
func parseCounted(msg string) (phase string, seq int) {
	for _, part := range strings.Split(msg, ";") {
		if v, ok := strings.CutPrefix(part, "phase="); ok {
			phase = v
		}
		if v, ok := strings.CutPrefix(part, "seq="); ok {
			seq, _ = strconv.Atoi(v)
		}
	}
	return phase, seq
}

// ReconcileFinding aggregates terminal-phase counts, applies revival
// corrections, releases finalizers on deletion, and enforces the TTL.
func (r *Reconciler) ReconcileFinding(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var fnd v1alpha1.Finding
	if err := r.Get(ctx, req.NamespacedName, &fnd); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	deleting := !fnd.DeletionTimestamp.IsZero()
	terminal := v1alpha1.Terminal(fnd.Status.Phase) && fnd.Status.CompletedAt != nil
	if !terminal && !deleting {
		return ctrl.Result{}, nil
	}

	phaseKey := stats.PhaseKey(fnd.Status.Phase)
	if deleting && !terminal {
		phaseKey = "deleted" // operator delete mid-flight: spend was real
	}
	repoKey := ""
	if fnd.Spec.Repository != nil {
		repoKey = fnd.Spec.Repository.Name
	}
	recommendation := ""
	if fnd.Status.Investigation != nil {
		recommendation = string(fnd.Status.Investigation.Recommendation)
	}
	attempts := int64(fnd.Status.Attempts.Investigation + fnd.Status.Attempts.Remediation)
	month := r.now().UTC().Format("2006-01")

	counts := findingCounts{
		phase: phaseKey, repo: repoKey, recommendation: recommendation,
		attempts: attempts, month: month,
	}
	changed, err := r.countFinding(ctx, &fnd, counts)
	if err != nil {
		return ctrl.Result{}, err
	}
	if changed {
		if err := r.Status().Update(ctx, &fnd); err != nil {
			if kerrors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, err
		}
	}

	if deleting {
		return ctrl.Result{}, r.releaseFinalizers(ctx, &fnd, fnd.Status.Conditions)
	}
	return r.enforceTTL(ctx, &fnd)
}

// findingCounts carries one finding's terminal-count inputs.
type findingCounts struct {
	phase, repo, recommendation, month string
	attempts                           int64
}

// countFinding aggregates the finding's terminal counts into the total and
// repository scopes, reporting whether conditions changed.
func (r *Reconciler) countFinding(ctx context.Context, fnd *v1alpha1.Finding, c findingCounts) (bool, error) {
	targets := []scopeTarget{
		{
			scope:     v1alpha1.RollupScope{Type: v1alpha1.ScopeTotal},
			condition: v1alpha1.ConditionRolledUpTotal,
			finalizer: v1alpha1.FinalizerRollupTotal,
		},
		{
			scope:     v1alpha1.RollupScope{Type: v1alpha1.ScopeRepository, Key: c.repo},
			condition: v1alpha1.ConditionRolledUpRepository,
			finalizer: v1alpha1.FinalizerRollupRepository,
		},
	}
	changed := false
	for _, target := range targets {
		cond := meta.FindStatusCondition(fnd.Status.Conditions, target.condition)
		counted, seq := "", 0
		if cond != nil && cond.Status == metav1.ConditionTrue {
			counted, seq = parseCounted(cond.Message)
			if counted == c.phase {
				continue // already counted under this phase
			}
		}
		if target.scope.Type == v1alpha1.ScopeRepository && c.repo == "" {
			meta.SetStatusCondition(&fnd.Status.Conditions, metav1.Condition{
				Type: target.condition, Status: metav1.ConditionTrue, Reason: "NoScopeKey",
			})
			changed = true
			continue
		}
		first := seq == 0
		delta := stats.FindingDelta{
			Phase:          c.phase,
			PrevPhase:      counted,
			Recommendation: c.recommendation,
			FirstCount:     first,
		}
		if first {
			delta.Attempts = c.attempts
		}
		ledger := fmt.Sprintf("f:%s:%d", fnd.UID, seq+1)
		if err := r.applyScope(ctx, target.scope, ledger, nil, &delta, c.month); err != nil {
			return changed, err
		}
		if target.scope.Type == v1alpha1.ScopeTotal && first {
			stats.RecordCompletion(ctx, c.phase, c.recommendation, c.repo)
		}
		meta.SetStatusCondition(&fnd.Status.Conditions, metav1.Condition{
			Type:    target.condition,
			Status:  metav1.ConditionTrue,
			Reason:  "Aggregated",
			Message: countedMarker(c.phase, seq+1),
		})
		changed = true
	}
	return changed, nil
}

// enforceTTL deletes the finding once completedAt + TTL elapses.
func (r *Reconciler) enforceTTL(ctx context.Context, fnd *v1alpha1.Finding) (ctrl.Result, error) {
	ttl := r.TTL
	if ttl < 0 {
		ttl = DefaultTTL
	}
	if ttl == 0 || fnd.Status.CompletedAt == nil {
		return ctrl.Result{}, nil
	}
	expiry := fnd.Status.CompletedAt.Add(ttl)
	if wait := expiry.Sub(r.now()); wait > 0 {
		return ctrl.Result{RequeueAfter: wait}, nil
	}
	if err := r.Delete(ctx, fnd); err != nil && !kerrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	stats.RecordDeleted(ctx, "ttl")
	r.log().LogAttrs(ctx, slog.LevelInfo, "finding expired",
		slog.String("finding", fnd.Name),
		slog.Time("completedAt", fnd.Status.CompletedAt.Time))
	return ctrl.Result{}, nil
}

// releaseFinalizers strips each rollup finalizer whose scope condition is
// true.
func (r *Reconciler) releaseFinalizers(ctx context.Context, obj client.Object, conditions []metav1.Condition) error {
	byFinalizer := map[string]string{
		v1alpha1.FinalizerRollupTotal:      v1alpha1.ConditionRolledUpTotal,
		v1alpha1.FinalizerRollupRepository: v1alpha1.ConditionRolledUpRepository,
		v1alpha1.FinalizerRollupHarness:    v1alpha1.ConditionRolledUpHarness,
		v1alpha1.FinalizerRollupModel:      v1alpha1.ConditionRolledUpModel,
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cur := obj.DeepCopyObject().(client.Object)
		if err := r.Get(ctx, client.ObjectKeyFromObject(obj), cur); err != nil {
			return client.IgnoreNotFound(err)
		}
		fins := cur.GetFinalizers()
		out := fins[:0]
		for _, f := range fins {
			cond, tracked := byFinalizer[f]
			if tracked && meta.IsStatusConditionTrue(conditions, cond) {
				continue // released
			}
			out = append(out, f)
		}
		if len(out) == len(fins) {
			return nil
		}
		cur.SetFinalizers(out)
		return r.Update(ctx, cur)
	})
}

// applyScope folds one delta into the scope's rollup object under conflict
// retry, creating the object on demand.
func (r *Reconciler) applyScope(
	ctx context.Context, scope v1alpha1.RollupScope, ledgerKey string,
	stage *stats.StageDelta, finding *stats.FindingDelta, month string,
) error {
	if scope.Type != v1alpha1.ScopeTotal {
		month = "" // the monthly trend line lives on the total scope only
	}
	name := stats.ScopeObjectName(scope)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var cur v1alpha1.FindingRollup
		err := r.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: name}, &cur)
		if kerrors.IsNotFound(err) {
			cur = v1alpha1.FindingRollup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: r.Namespace,
					Labels:    map[string]string{v1alpha1.LabelScope: string(scope.Type)},
				},
				Spec: v1alpha1.FindingRollupSpec{Scope: scope},
			}
			if err := r.Create(ctx, &cur); err != nil && !kerrors.IsAlreadyExists(err) {
				return fmt.Errorf("create rollup %s: %w", name, err)
			}
			if err := r.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: name}, &cur); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if !stats.Apply(&cur.Status, ledgerKey, stage, finding, r.now(), month) {
			return nil // already applied (crash between rollup write and marker)
		}
		return r.Status().Update(ctx, &cur)
	})
}

// SetupWithManager registers the three aggregation controllers.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Investigation{}).
		Named("rollup-investigation").
		Complete(reconcile.Func(r.ReconcileInvestigation)); err != nil {
		return err
	}
	if err := ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Remediation{}).
		Named("rollup-remediation").
		Complete(reconcile.Func(r.ReconcileRemediation)); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Finding{}).
		Named("rollup-finding").
		Complete(reconcile.Func(r.ReconcileFinding))
}

func (r *Reconciler) now() time.Time {
	if r.Now == nil {
		return time.Now()
	}
	return r.Now()
}

func (r *Reconciler) log() *slog.Logger {
	if r.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return r.Log
}
