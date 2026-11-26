// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package integration

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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/ghas"
	"github.com/bitwise-media-group/patchy/internal/ghclient"
	"github.com/bitwise-media-group/patchy/pkg/source"
)

// Backfill request timing.
const (
	// DefaultBackfillTTL is how long a finished (or never-decided) request
	// is kept. The status page's backfill cooldown reads finished requests,
	// so the TTL must outlast the cooldown.
	DefaultBackfillTTL = 7 * 24 * time.Hour
	// maxBackfillAttempts bounds the retries of a walk that keeps failing
	// (credentials, the API) before the request fails for good.
	maxBackfillAttempts = 5
	// backfillRetryBase and backfillRetryCap pace the retries: 1m, 2m, 4m,
	// … up to 30m.
	backfillRetryBase = time.Minute
	backfillRetryCap  = 30 * time.Minute
	// backfillWaitRetry paces the re-check of a request held by its
	// Integration (suspended, or its credential not Ready).
	backfillWaitRetry = 5 * time.Minute
)

// BackfillReconciler walks BackfillRequests: one bounded list-alerts walk
// per request, through the named Integration's credential, ingesting every
// listed alert (ingestion is idempotent: an alert a live finding already
// tracks changes nothing). It is the single writer of BackfillRequest
// status, and deletes finished requests once their TTL has passed. The
// work queue runs one reconcile at a time, so walks never overlap.
type BackfillReconciler struct {
	client.Client
	// Creds builds the GitHub listers.
	Creds *Creds
	// Ingest folds the listed alerts into Findings.
	Ingest *Ingestor
	// TTL is how long a finished request is kept; <= 0 means
	// DefaultBackfillTTL.
	TTL time.Duration
	// Log receives reconcile diagnostics; nil discards.
	Log *slog.Logger
	// Now tells time; nil means time.Now (tests override).
	Now func() time.Time
	// ListerFor overrides backfill-lister construction in tests; nil means
	// the ghas lister built from the Integration's credential. ok=false
	// reports a provider with no listing capability.
	ListerFor func(ctx context.Context, integ *v1alpha1.Integration) (lister source.Lister, ok bool, err error)
}

// SetupWithManager wires the reconciler. Integration changes wake the
// requests they hold: one filed before its Integration's first credential
// check, or while it was suspended, runs as soon as the Integration is
// Ready rather than at the next hold retry.
func (r *BackfillReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.BackfillRequest{}).
		Watches(&v1alpha1.Integration{}, handler.EnqueueRequestsFromMapFunc(r.requestsFor)).
		Named("backfillrequest").
		Complete(r)
}

// requestsFor maps an Integration onto its unfinished BackfillRequests.
func (r *BackfillReconciler) requestsFor(ctx context.Context, obj client.Object) []reconcile.Request {
	var list v1alpha1.BackfillRequestList
	if err := r.List(ctx, &list, client.InNamespace(obj.GetNamespace())); err != nil {
		r.log().LogAttrs(ctx, slog.LevelWarn, "list backfill requests for integration",
			slog.String("integration", obj.GetName()), slog.Any("error", err))
		return nil
	}
	var out []reconcile.Request
	for i := range list.Items {
		b := &list.Items[i]
		if b.Spec.IntegrationRef.Name == obj.GetName() && !b.Status.Phase.Terminal() {
			out = append(out, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(b)})
		}
	}
	return out
}

func (r *BackfillReconciler) log() *slog.Logger {
	if r.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return r.Log
}

func (r *BackfillReconciler) now() time.Time {
	if r.Now == nil {
		return time.Now()
	}
	return r.Now()
}

func (r *BackfillReconciler) ttl() time.Duration {
	if r.TTL <= 0 {
		return DefaultBackfillTTL
	}
	return r.TTL
}

// Reconcile advances one BackfillRequest.
func (r *BackfillReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var bfr v1alpha1.BackfillRequest
	if err := r.Get(ctx, req.NamespacedName, &bfr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !bfr.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	now := r.now()
	st := &bfr.Status

	if st.Phase.Terminal() {
		return r.expire(ctx, &bfr, st.CompletedAt.Time, now)
	}

	// A request filed without a grant waits for an operator's decision.
	if bfr.Spec.Via == v1alpha1.BackfillViaRequested {
		switch d := bfr.Spec.Decision; {
		case d == nil:
			if st.Phase != v1alpha1.BackfillAwaitingApproval {
				st.Phase = v1alpha1.BackfillAwaitingApproval
				return r.writeStatus(ctx, &bfr, ctrl.Result{})
			}
			// Undecided requests expire on the same TTL, from creation.
			return r.expire(ctx, &bfr, bfr.CreationTimestamp.Time, now)
		case d.Verdict == v1alpha1.BackfillDeniedVerdict:
			st.Phase, st.CompletedAt, st.Error = v1alpha1.BackfillDenied, &metav1.Time{Time: now}, d.Reason
			r.log().LogAttrs(ctx, slog.LevelInfo, "backfill request denied",
				slog.String("request", bfr.Name), slog.String("by", d.By))
			return r.writeStatus(ctx, &bfr, ctrl.Result{RequeueAfter: r.ttl()})
		}
	}

	var integ v1alpha1.Integration
	key := types.NamespacedName{Namespace: bfr.Namespace, Name: bfr.Spec.IntegrationRef.Name}
	if err := r.Get(ctx, key, &integ); err != nil {
		if !kerrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		return r.fail(ctx, &bfr, now, fmt.Sprintf("integration %s not found", key.Name))
	}
	// A suspended Integration, or one whose credential is not Ready, holds
	// the request without spending an attempt.
	if integ.Spec.Suspend {
		return r.hold(ctx, &bfr, "integration "+integ.Name+" is suspended")
	}
	if ready := meta.FindStatusCondition(integ.Status.Conditions, v1alpha1.ConditionReady); ready == nil ||
		ready.Status != metav1.ConditionTrue {
		msg := "integration " + integ.Name + " is not ready"
		if ready != nil && ready.Message != "" {
			msg += ": " + ready.Message
		}
		return r.hold(ctx, &bfr, msg)
	}

	st.Attempts++
	st.LastAttemptAt = &metav1.Time{Time: now}
	out, ran := r.runBackfill(ctx, &integ, bfr.Spec.Repositories)
	st.Listed, st.Ingested, st.Truncated, st.Error = out.Listed, out.Ingested, out.Truncated, out.Error
	switch {
	case ran:
		st.Phase, st.CompletedAt = v1alpha1.BackfillComplete, &metav1.Time{Time: now}
		r.log().LogAttrs(ctx, slog.LevelInfo, "backfill request walked",
			slog.String("request", bfr.Name), slog.String("integration", integ.Name),
			slog.Int("listed", int(st.Listed)), slog.Int("ingested", int(st.Ingested)),
			slog.Bool("truncated", st.Truncated))
		return r.writeStatus(ctx, &bfr, ctrl.Result{RequeueAfter: r.ttl()})
	case out.Final || st.Attempts >= maxBackfillAttempts:
		return r.fail(ctx, &bfr, now, st.Error)
	}
	st.Phase = v1alpha1.BackfillPending
	r.log().LogAttrs(ctx, slog.LevelWarn, "backfill walk failed; retrying",
		slog.String("request", bfr.Name), slog.Int("attempt", int(st.Attempts)), slog.String("error", st.Error))
	return r.writeStatus(ctx, &bfr, ctrl.Result{RequeueAfter: backfillBackoff(st.Attempts)})
}

// backfillBackoff is the wait before retry n+1.
func backfillBackoff(attempts int32) time.Duration {
	d := backfillRetryBase
	for i := int32(1); i < attempts && d < backfillRetryCap; i++ {
		d *= 2
	}
	return min(d, backfillRetryCap)
}

// hold parks a request its Integration cannot run yet.
func (r *BackfillReconciler) hold(ctx context.Context, bfr *v1alpha1.BackfillRequest, msg string) (ctrl.Result, error) {
	if bfr.Status.Phase == v1alpha1.BackfillPending && bfr.Status.Error == msg {
		return ctrl.Result{RequeueAfter: backfillWaitRetry}, nil
	}
	bfr.Status.Phase, bfr.Status.Error = v1alpha1.BackfillPending, msg
	return r.writeStatus(ctx, bfr, ctrl.Result{RequeueAfter: backfillWaitRetry})
}

// fail settles a request that will never run.
func (r *BackfillReconciler) fail(
	ctx context.Context, bfr *v1alpha1.BackfillRequest, now time.Time, msg string,
) (ctrl.Result, error) {
	bfr.Status.Phase, bfr.Status.CompletedAt, bfr.Status.Error = v1alpha1.BackfillFailed, &metav1.Time{Time: now}, msg
	r.log().LogAttrs(ctx, slog.LevelWarn, "backfill request failed",
		slog.String("request", bfr.Name), slog.String("error", msg))
	return r.writeStatus(ctx, bfr, ctrl.Result{RequeueAfter: r.ttl()})
}

// expire deletes a request whose TTL, counted from since, has passed, and
// otherwise requeues for that moment.
func (r *BackfillReconciler) expire(
	ctx context.Context, bfr *v1alpha1.BackfillRequest, since, now time.Time,
) (ctrl.Result, error) {
	if left := since.Add(r.ttl()).Sub(now); left > 0 {
		return ctrl.Result{RequeueAfter: left}, nil
	}
	if err := r.Delete(ctx, bfr); err != nil && !kerrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *BackfillReconciler) writeStatus(
	ctx context.Context, bfr *v1alpha1.BackfillRequest, res ctrl.Result,
) (ctrl.Result, error) {
	if err := r.Status().Update(ctx, bfr); err != nil {
		if kerrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}
	return res, nil
}

// backfillOutcome is one walk's result.
type backfillOutcome struct {
	Listed, Ingested int32
	Truncated        bool
	Error            string
	// Final marks a failure no retry can cure (the provider cannot list).
	Final bool
}

// runBackfill performs one bounded list-alerts walk over repos (empty means
// the credential's full scope). One stuck alert never starves the rest; the
// last ingest error is reported. ran is false when the walk itself failed
// (credentials, a fixable filter, the API), which the caller retries.
func (r *BackfillReconciler) runBackfill(
	ctx context.Context, integ *v1alpha1.Integration, repos []string,
) (out backfillOutcome, ran bool) {
	lister, ok, err := r.listerFor(ctx, integ)
	if err != nil {
		out.Error = err.Error()
		return out, false
	}
	if !ok {
		// No amount of retrying teaches a provider to list its findings.
		out.Error, out.Final = fmt.Sprintf("provider %s does not support backfill", integ.Spec.Provider), true
		return out, false
	}

	var lastErr error
	complete, err := lister.List(ctx, repos, func(f source.Finding) bool {
		out.Listed++
		if err := r.Ingest.Ingest(ctx, integ, f); err != nil {
			lastErr = err
			r.log().LogAttrs(ctx, slog.LevelWarn, "backfill ingest failed",
				slog.String("integration", integ.Name),
				slog.String("repository", f.Repo.String()),
				slog.Any("error", err))
			return true
		}
		out.Ingested++
		return true
	})
	if err != nil {
		out.Error = err.Error()
		return out, false
	}
	out.Truncated = !complete
	if !complete {
		r.log().LogAttrs(ctx, slog.LevelWarn, "backfill truncated at the page budget",
			slog.String("integration", integ.Name),
			slog.Int("listed", int(out.Listed)))
	}
	if lastErr != nil {
		out.Error = lastErr.Error()
	}
	return out, true
}

// listerFor resolves the backfill lister for the Integration: the ListerFor
// hook when set (tests), else a lister over every GitHub capability the
// Integration enables (code scanning, secret scanning, or both), built from
// the credential (the App installation fan-out, or the exact-repository PAT
// walk). ok is false when the provider has no listing capability at all.
func (r *BackfillReconciler) listerFor(
	ctx context.Context, integ *v1alpha1.Integration,
) (lister source.Lister, ok bool, err error) {
	if r.ListerFor != nil {
		return r.ListerFor(ctx, integ)
	}
	if integ.Spec.Provider != v1alpha1.IntegrationProviderGitHub {
		return nil, false, nil
	}
	code, secrets := codeScanningEnabled(integ), secretScanningEnabled(integ)
	if !code && !secrets {
		return nil, true, errors.New("backfill needs spec.github.codeScanningAlerts or " +
			"spec.github.secretScanningAlerts enabled; there is no source to ingest into")
	}
	app, isApp, err := r.Creds.App(ctx, integ)
	if err != nil {
		return nil, true, err
	}
	var c *ghclient.Client
	if !isApp {
		// A PAT client is repository-independent; the enumerators require
		// the request to name exact repositories, since a PAT cannot
		// discover any.
		if c, err = r.Creds.Client(ctx, integ, ghclient.Repo{}); err != nil {
			return nil, true, err
		}
	}
	var listers multiLister
	if code {
		if isApp {
			listers = append(listers, ghas.NewLister(&ghclient.AppAlertEnumerator{App: app}))
		} else {
			listers = append(listers, ghas.NewLister(&ghclient.PATAlertEnumerator{Client: c}))
		}
	}
	if secrets {
		if isApp {
			listers = append(listers, ghas.NewSecretsLister(&ghclient.AppSecretAlertEnumerator{App: app}))
		} else {
			listers = append(listers, ghas.NewSecretsLister(&ghclient.PATSecretAlertEnumerator{Client: c}))
		}
	}
	return listers, true, nil
}

// multiLister walks several sources' listings in turn as one backfill. It
// is complete only when every listing is; a stopped yield ends the whole
// walk, as it would a single listing.
type multiLister []source.Lister

// List implements source.Lister.
func (m multiLister) List(
	ctx context.Context, repos []string, yield func(source.Finding) bool,
) (complete bool, err error) {
	complete = true
	for _, l := range m {
		stopped := false
		c, err := l.List(ctx, repos, func(f source.Finding) bool {
			if !yield(f) {
				stopped = true
				return false
			}
			return true
		})
		if err != nil {
			return false, err
		}
		if stopped {
			return false, nil
		}
		complete = complete && c
	}
	return complete, nil
}
