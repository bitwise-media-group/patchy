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

// holdNotes explains why a finding is waiting, in the terms a human needs to
// act on it — the reason names alone do not say what is being decided. A hold
// can have several reasons at once, so they are joined rather than ranked.
//
// The fallback covers an investigation stamped by an older controller, which
// set the flag without recording why; breaking-change was the only hold that
// existed then.
func holdNotes(inv *v1alpha1.Investigation) string {
	if !inv.Status.AwaitApproval {
		return ""
	}
	reasons := inv.Status.HoldReasons
	if len(reasons) == 0 {
		reasons = []v1alpha1.HoldReason{v1alpha1.HoldBreakingChangeAvailable}
	}
	parts := make([]string, 0, len(reasons))
	for _, r := range reasons {
		parts = append(parts, holdText(r, inv.Status.RemediationParameters))
	}
	return "awaiting approval — " + strings.Join(parts, "; ")
}

// holdText renders one reason, quantifying the budget holds: "needs more
// turns" is not actionable, "predicts 140 turns" is.
func holdText(r v1alpha1.HoldReason, p *v1alpha1.AgentParameters) string {
	var est *v1alpha1.AgentEstimate
	if p != nil {
		est = p.Estimate
	}
	switch r {
	case v1alpha1.HoldBreakingChangeAvailable:
		return "a better fix exists but it breaks compatibility"
	case v1alpha1.HoldLowConfidence:
		return "confidence is below the automation threshold"
	case v1alpha1.HoldEstimateExceedsTurnCeiling:
		if est != nil {
			return fmt.Sprintf("the fix is predicted to need %d turns, over the ceiling", est.MaxTurns)
		}
		return "the fix is predicted to need more turns than the ceiling allows"
	case v1alpha1.HoldEstimateExceedsTokenCeiling:
		if est != nil {
			return fmt.Sprintf("the fix is predicted to need %d output tokens, over the ceiling", est.TokenBudget)
		}
		return "the fix is predicted to need more output tokens than the ceiling allows"
	default:
		return string(r)
	}
}

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
	if note := holdNotes(inv); note != "" {
		d.Field("Hold", note)
	}
	if p := inv.Status.RemediationParameters; p != nil && p.Estimate != nil {
		d.Fieldf("Estimated fix", "%d turns, %d output tokens (a prediction, not a limit)",
			p.Estimate.MaxTurns, p.Estimate.TokenBudget)
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
	if note := holdNotes(inv); note != "" {
		d.Field("Hold", note)
	}
	d.Body(Body(inv.Status.Report, raw))
}
