// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package render

import (
	"fmt"
	"time"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/printer"
)

// RemediationDetail renders everything known about one remediation attempt.
func RemediationDetail(d *printer.Doc, rem *v1alpha1.Remediation, now time.Time) {
	d.Section(fmt.Sprintf("Remediation %s", rem.Name)).
		Field("Finding", rem.Spec.FindingRef.Name).
		Field("Investigation", rem.Spec.InvestigationRef.Name).
		Fieldf("Attempt", "%d", rem.Spec.Attempt).
		Fieldf("Queue priority", "%d", rem.Spec.Priority).
		Field("Phase", string(rem.Status.Phase)).
		Fieldf("Success", "%t", rem.Status.Success).
		Field("Confidence", rem.Status.Confidence).
		Field("Approved by", rem.Spec.ApprovedBy).
		Field("Granted", Timestamp(rem.Status.GrantedAt, now)).
		Field("Age", Age(rem.CreationTimestamp.Time, now))
	if rem.Spec.Revival {
		d.Field("Revival", "yes — remediate-only run reviving a handed-off finding")
	}

	d.Section("Changeset").
		Field("Branch", rem.Status.Branch).
		Field("Commit", rem.Status.PushedCommit)
	if pr := rem.Status.PullRequest; pr != nil {
		d.Fieldf("Pull request", "#%d %s", pr.Number, pr.URL)
	}

	Stage(d, rem.Status.Stage, now)
	Budget(d, rem.Spec.Parameters, rem.Status.Stage)
}

// Budget renders predicted against granted against actual. The estimate never
// limits a run — it only decides whether a human had to approve it — so the
// grant is shown beside it: without both, a run that finished well inside a
// generous grant looks identical to one that scraped a tight estimate.
func Budget(d *printer.Doc, p v1alpha1.AgentParameters, st *v1alpha1.StageResult) {
	if p.MaxTurns == 0 && p.TokenBudget == 0 && p.Estimate == nil {
		return
	}
	sec := d.Section("Budget")
	var turns, tokens int64
	if st != nil {
		turns, tokens = int64(st.NumTurns), st.Usage.OutputTokens
	}
	sec.Field("Turns", budgetLine(estimateTurns(p.Estimate), int64(p.MaxTurns), turns))
	sec.Field("Output tokens", budgetLine(estimateTokens(p.Estimate), p.TokenBudget, tokens))
}

func estimateTurns(e *v1alpha1.AgentEstimate) int64 {
	if e == nil {
		return 0
	}
	return int64(e.MaxTurns)
}

func estimateTokens(e *v1alpha1.AgentEstimate) int64 {
	if e == nil {
		return 0
	}
	return e.TokenBudget
}

// budgetLine renders "34 of 80 granted (est. 12, +183%)". The skew is omitted
// when there was no prediction to be right or wrong about.
func budgetLine(predicted, granted, actual int64) string {
	line := fmt.Sprintf("%d of %d granted", actual, granted)
	if predicted <= 0 {
		return line + " (no estimate)"
	}
	return fmt.Sprintf("%s (est. %d, %s)", line, predicted, SkewPercent(predicted, actual))
}

// SkewPercent renders how far actual ran over (+) or under (-) predicted.
// The sign is the point — it says which way the estimate was wrong.
func SkewPercent(predicted, actual int64) string {
	if predicted <= 0 {
		return Dash("")
	}
	pct := (actual - predicted) * 100 / predicted
	if pct == 0 {
		return "on target"
	}
	if pct > 0 {
		return fmt.Sprintf("+%d%%", pct)
	}
	return fmt.Sprintf("%d%%", pct)
}

// RemediationReview renders the result and the report.
func RemediationReview(d *printer.Doc, rem *v1alpha1.Remediation, raw bool) {
	d.Section(fmt.Sprintf("Remediation %s (attempt %d)", rem.Name, rem.Spec.Attempt)).
		Fieldf("Success", "%t", rem.Status.Success).
		Field("Confidence", rem.Status.Confidence).
		Field("Branch", rem.Status.Branch).
		Field("Commit", rem.Status.PushedCommit).
		Field("Outcome", Outcome(rem.Status.Stage)).
		Field("Cost", Cost(rem.Status.Stage))
	if pr := rem.Status.PullRequest; pr != nil {
		d.Fieldf("Pull request", "#%d %s", pr.Number, pr.URL)
	}
	d.Body(Body(rem.Status.Report, raw))
}
