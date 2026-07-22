// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package integration

import (
	"context"
	"log/slog"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/ghclient"
)

const (
	// defaultLookback is the sweep's scan horizon when spec omits one.
	defaultLookback = 24 * time.Hour
	// maxRedeliveryAttempts caps the attempts (original plus redeliveries)
	// visible in the lookback window before the sweep gives a delivery up.
	// Evidence ages out with the window, so a permanently failing delivery
	// degrades to roughly one retry per lookback period rather than
	// retrying every sweep for GitHub's full 30-day retention.
	maxRedeliveryAttempts = 3
)

// sweepDeliveries scans the App webhook's delivery log over the lookback
// window and asks GitHub to redeliver: every delivery that never got a 2xx
// (the standing sweep), or — replayAll, the demo reset path — every
// delivery regardless of outcome. Duplicates are safe either way: the
// receiver dedups per delivery GUID and ingestion is idempotent. The sweep
// never fails the reconcile; the outcome (including errors) lands on the
// returned status. scanned reports whether the log walk itself succeeded —
// false means nothing was attempted and the caller should not consider a
// requested replay handled.
func (r *IntegrationReconciler) sweepDeliveries(
	ctx context.Context, integ *v1alpha1.Integration, now time.Time, replayAll bool,
) (st *v1alpha1.RedeliveryStatus, scanned bool) {
	st = &v1alpha1.RedeliveryStatus{LastSweepAt: &metav1.Time{Time: now}}

	app, ok, err := r.Creds.App(ctx, integ)
	if err != nil {
		st.Error = err.Error()
		return st, false
	}
	if !ok {
		st.Error = "redelivery requires GitHub App credentials; the delivery log is not visible to a PAT"
		return st, false
	}

	lookback := defaultLookback
	if gh := integ.Spec.GitHub; gh != nil && gh.Redelivery != nil && gh.Redelivery.Lookback.Duration > 0 {
		lookback = gh.Redelivery.Lookback.Duration
	}
	horizon := now.Add(-lookback)

	var window []ghclient.Delivery
	complete, err := app.Deliveries(ctx, func(d ghclient.Delivery) bool {
		if d.DeliveredAt.Before(horizon) {
			return false
		}
		window = append(window, d)
		return true
	})
	if err != nil {
		st.Error = err.Error()
		return st, false
	}
	st.Scanned = int32(len(window))
	st.Truncated = !complete
	if !complete {
		r.log().LogAttrs(ctx, slog.LevelWarn, "delivery sweep truncated at the page cap",
			slog.String("integration", integ.Name),
			slog.Int("scanned", len(window)),
			slog.Duration("lookback", lookback))
	}

	picked := pickRedeliveries(window, maxRedeliveryAttempts)
	if replayAll {
		picked = pickReplays(window)
	}
	for _, id := range picked {
		if err := app.Redeliver(ctx, id); err != nil {
			// Keep going: one stuck delivery must not starve the rest. The
			// last error is surfaced; the next sweep retries naturally.
			st.Error = err.Error()
			r.log().LogAttrs(ctx, slog.LevelWarn, "redelivery failed",
				slog.String("integration", integ.Name),
				slog.Int64("delivery", id),
				slog.Any("error", err))
			continue
		}
		st.Redelivered++
	}
	return st, true
}

// pendingReplay returns the spec.replay request not yet echoed by
// status.redelivery.replayedAt, or nil.
func pendingReplay(integ *v1alpha1.Integration) *v1alpha1.ActionRequest {
	rep := integ.Spec.Replay
	if rep == nil {
		return nil
	}
	if red := integ.Status.Redelivery; red != nil && red.ReplayedAt != nil && !red.ReplayedAt.Time.Before(rep.At.Time) {
		return nil
	}
	return rep
}

// pendingReset returns the spec.reset request not yet echoed by
// status.resetAt, or nil.
func pendingReset(integ *v1alpha1.Integration) *v1alpha1.ActionRequest {
	req := integ.Spec.Reset
	if req == nil {
		return nil
	}
	if at := integ.Status.ResetAt; at != nil && !at.Time.Before(req.At.Time) {
		return nil
	}
	return req
}

// pickRedeliveries selects, from one scan window of delivery attempts, the
// attempt to redeliver per logical delivery (GUID): the newest failed
// attempt of any GUID with no successful attempt and fewer than maxAttempts
// attempts in the window. deliveries must be newest-first, as the log walk
// yields them.
func pickRedeliveries(deliveries []ghclient.Delivery, maxAttempts int) []int64 {
	type tally struct {
		newestFailed int64
		attempts     int
		succeeded    bool
	}
	seen := make(map[string]*tally)
	order := make([]string, 0, len(deliveries))
	for _, d := range deliveries {
		t := seen[d.GUID]
		if t == nil {
			t = &tally{newestFailed: -1}
			seen[d.GUID] = t
			order = append(order, d.GUID)
		}
		t.attempts++
		if d.OK {
			t.succeeded = true
		} else if t.newestFailed < 0 {
			t.newestFailed = d.ID
		}
	}
	var out []int64
	for _, guid := range order {
		t := seen[guid]
		if t.succeeded || t.newestFailed < 0 || t.attempts >= maxAttempts {
			continue
		}
		out = append(out, t.newestFailed)
	}
	return out
}

// pickReplays selects the newest attempt of every logical delivery (GUID)
// in the window, successful or not — the full replay. deliveries must be
// newest-first.
func pickReplays(deliveries []ghclient.Delivery) []int64 {
	seen := make(map[string]bool)
	var out []int64
	for _, d := range deliveries {
		if seen[d.GUID] {
			continue
		}
		seen[d.GUID] = true
		out = append(out, d.ID)
	}
	return out
}
