// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package render

import (
	"fmt"
	"strings"
	"time"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/printer"
)

// BackfillRequestDetail renders everything known about one backfill request:
// what was asked for and by whom, the operator's decision when the request
// needed one, and the outcome of the walk.
func BackfillRequestDetail(d *printer.Doc, b *v1alpha1.BackfillRequest, now time.Time) {
	d.Section(fmt.Sprintf("Backfill request %s", b.Name)).
		Field("Integration", b.Spec.IntegrationRef.Name).
		Field("Requested by", b.Spec.RequestedBy).
		Field("Via", BackfillVia(b)).
		Field("Phase", string(b.Status.Phase)).
		Field("Repositories", BackfillRepositories(b)).
		Field("Age", Age(b.CreationTimestamp.Time, now))
	if gh := b.Spec.GitHub; gh != nil {
		d.Fieldf("GitHub", "%s (%s)", gh.Login, Dash(gh.Role))
	}

	d.Section("Decision")
	switch dec := b.Spec.Decision; {
	case dec != nil:
		d.Field("Verdict", string(dec.Verdict)).
			Field("By", dec.By).
			Field("At", Timestamp(&dec.At, now)).
			Field("Reason", dec.Reason)
	case b.Spec.Via == v1alpha1.BackfillViaRequested:
		d.Field("Verdict", "awaiting an operator").
			Fieldf("Approve with", "patchy approve backfillrequest %s", b.Name).
			Fieldf("Deny with", "patchy deny backfillrequest %s --reason ...", b.Name)
	}

	d.Section("Outcome").
		Field("Summary", BackfillOutcome(b))
	if b.Status.Attempts > 0 {
		d.Fieldf("Attempts", "%d", b.Status.Attempts)
	}
	d.Field("Last attempt", Timestamp(b.Status.LastAttemptAt, now)).
		Field("Completed", Timestamp(b.Status.CompletedAt, now)).
		Field("Error", b.Status.Error)
	if b.Status.Truncated {
		d.Field("Truncated", "yes: the walk hit the page budget; request again with a narrower --repo prefix")
	}
}

// BackfillVia names the grant that authorized the request. An empty via is
// the backfill grant itself.
func BackfillVia(b *v1alpha1.BackfillRequest) string {
	if b.Spec.Via == "" {
		return string(v1alpha1.BackfillViaGrant)
	}
	return string(b.Spec.Via)
}

// BackfillRepositories renders the request's repository filter. No filter
// means the credential's whole scope, which is worth saying out loud.
func BackfillRepositories(b *v1alpha1.BackfillRequest) string {
	if len(b.Spec.Repositories) == 0 {
		return "all (the credential's full scope)"
	}
	return strings.Join(b.Spec.Repositories, ", ")
}

// BackfillOutcome summarises the walk's counts on one line, or "" before the
// walk has run.
func BackfillOutcome(b *v1alpha1.BackfillRequest) string {
	st := b.Status
	if st.Attempts == 0 && st.Listed == 0 && st.Ingested == 0 {
		return ""
	}
	s := fmt.Sprintf("%d listed, %d ingested", st.Listed, st.Ingested)
	if st.Truncated {
		s += " (truncated)"
	}
	return s
}
