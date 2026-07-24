// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package render

import (
	"fmt"
	"time"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/printer"
)

// holdNote explains status.awaitApproval in the terms a human needs to act on
// it — the field name alone does not say why anything is waiting.
const holdNote = "awaiting approval — a better fix exists but it breaks compatibility"

// InvestigationDetail renders everything known about one analysis attempt.
func InvestigationDetail(d *printer.Doc, inv *v1alpha1.Investigation, now time.Time) {
	d.Section(fmt.Sprintf("Investigation %s", inv.Name)).
		Field("Finding", inv.Spec.FindingRef.Name).
		Fieldf("Attempt", "%d", inv.Spec.Attempt).
		Field("Phase", string(inv.Status.Phase)).
		Field("Verdict", string(inv.Status.Recommendation)).
		Field("Confidence", inv.Status.Confidence).
		Field("Severity", string(inv.Status.Severity)).
		Field("Priority", string(inv.Status.Priority)).
		Field("Age", Age(inv.CreationTimestamp.Time, now))
	if inv.Status.AwaitApproval {
		d.Field("Hold", holdNote)
	}

	d.Section("Analysis").
		Field("Exploitability", Rating(inv.Status.Exploitability)).
		Field("Likelihood", Rating(inv.Status.Likelihood)).
		Field("Impact", Rating(inv.Status.Impact))

	Stage(d, inv.Status.Stage, now)
}

// InvestigationReview renders the verdict and the report — what a human reads
// when deciding whether to act on the agent's conclusion.
func InvestigationReview(d *printer.Doc, inv *v1alpha1.Investigation, raw bool) {
	d.Section(fmt.Sprintf("Investigation %s (attempt %d)", inv.Name, inv.Spec.Attempt)).
		Field("Verdict", string(inv.Status.Recommendation)).
		Field("Confidence", inv.Status.Confidence).
		Field("Severity", string(inv.Status.Severity)).
		Field("Exploitability", Rating(inv.Status.Exploitability)).
		Field("Likelihood", Rating(inv.Status.Likelihood)).
		Field("Impact", Rating(inv.Status.Impact)).
		Field("Outcome", Outcome(inv.Status.Stage)).
		Field("Cost", Cost(inv.Status.Stage))
	if inv.Status.AwaitApproval {
		d.Field("Hold", holdNote)
	}
	d.Body(Body(inv.Status.Report, raw))
}
