// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package integration

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/ghas"
	"github.com/bitwise-media-group/patchy/internal/ghclient"
	"github.com/bitwise-media-group/patchy/pkg/source"
)

// pendingBackfill returns the spec.backfill request not yet echoed by
// status.backfill.backfilledAt, or nil.
func pendingBackfill(integ *v1alpha1.Integration) *v1alpha1.BackfillRequest {
	req := integ.Spec.Backfill
	if req == nil {
		return nil
	}
	if bf := integ.Status.Backfill; bf != nil && bf.BackfilledAt != nil && !bf.BackfilledAt.Time.Before(req.At.Time) {
		return nil
	}
	return req
}

// runBackfill performs one bounded list-alerts walk for a pending backfill
// request, ingesting every listed alert (ingestion is idempotent, so
// alerts that already have findings just fold in as duplicates). It never
// fails the reconcile; the outcome (including errors) lands on the
// returned status. ran reports whether the request was consumed — false
// means the walk itself failed (credentials, a fixable filter, the API)
// and the next reconcile should try again; the caller echoes
// status.backfill.backfilledAt only when ran is true.
func (r *IntegrationReconciler) runBackfill(
	ctx context.Context, integ *v1alpha1.Integration, req *v1alpha1.BackfillRequest, now time.Time,
) (st *v1alpha1.BackfillStatus, ran bool) {
	st = &v1alpha1.BackfillStatus{LastRunAt: &metav1.Time{Time: now}}

	lister, ok, err := r.listerFor(ctx, integ)
	if err != nil {
		st.Error = err.Error()
		return st, false
	}
	if !ok {
		// Consumed, not retried: no amount of reconciling teaches a
		// provider to list its findings.
		st.Error = fmt.Sprintf("provider %s does not support backfill", integ.Spec.Provider)
		return st, true
	}

	var lastErr error
	complete, err := lister.List(ctx, req.Repositories, func(f source.Finding) bool {
		st.Listed++
		if err := r.Ingest.Ingest(ctx, integ, f); err != nil {
			// Keep going: one stuck alert must not starve the rest. The
			// last error is surfaced on status.
			lastErr = err
			r.log().LogAttrs(ctx, slog.LevelWarn, "backfill ingest failed",
				slog.String("integration", integ.Name),
				slog.String("repository", f.Repo.String()),
				slog.Any("error", err))
			return true
		}
		st.Ingested++
		return true
	})
	if err != nil {
		st.Error = err.Error()
		return st, false
	}
	st.Truncated = !complete
	if !complete {
		r.log().LogAttrs(ctx, slog.LevelWarn, "backfill truncated at the page budget",
			slog.String("integration", integ.Name),
			slog.Int("listed", int(st.Listed)))
	}
	if lastErr != nil {
		st.Error = lastErr.Error()
	}
	return st, true
}

// listerFor resolves the backfill lister for the Integration: the ListerFor
// seam when set (tests), else the ghas lister built from the credential —
// the App installation fan-out, or the exact-repository PAT walk. ok is
// false when the provider has no listing capability at all.
func (r *IntegrationReconciler) listerFor(
	ctx context.Context, integ *v1alpha1.Integration,
) (lister source.Lister, ok bool, err error) {
	if r.ListerFor != nil {
		return r.ListerFor(ctx, integ)
	}
	if integ.Spec.Provider != v1alpha1.IntegrationProviderGitHub {
		return nil, false, nil
	}
	if !codeScanningEnabled(integ) {
		return nil, true, errors.New(
			"backfill needs spec.github.codeScanningAlerts enabled; there is no source to ingest into")
	}
	app, isApp, err := r.Creds.App(ctx, integ)
	if err != nil {
		return nil, true, err
	}
	if isApp {
		return ghas.NewLister(&ghclient.AppAlertEnumerator{App: app}), true, nil
	}
	// A PAT client is repository-independent; the enumerator requires the
	// request to name exact repositories, since a PAT cannot discover any.
	c, err := r.Creds.Client(ctx, integ, ghclient.Repo{})
	if err != nil {
		return nil, true, err
	}
	return ghas.NewLister(&ghclient.PATAlertEnumerator{Client: c}), true, nil
}
