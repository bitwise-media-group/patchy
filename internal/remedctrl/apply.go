// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package remedctrl

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/bitwise-media-group/patchy/internal/envelope"
	"github.com/bitwise-media-group/patchy/internal/ghclient"
	"github.com/bitwise-media-group/patchy/internal/labels"
	"github.com/bitwise-media-group/patchy/internal/templates"
)

// The agent phases (mirrors agentrun.Phase without importing the runtime).
const (
	phaseFull      = "classify+remediate"
	phaseRemediate = "remediate"
)

// dismissReason is GitHub's enum value for a false-positive dismissal.
const dismissReason = "false positive"

// applyClassification lands the classification report and routes the issue —
// DESIGN step 6.
func (c *Controller) applyClassification(ctx context.Context, st Stores, ref issueRef,
	cls *envelope.Classification) error {
	report := cls.ReportMarkdown
	if report == "" {
		report = fmt.Sprintf("_Classification did not produce a report (%s): %s_", cls.Outcome, cls.Detail)
	}
	comment, err := templates.ReportComment(templates.ClassificationReportHeading, report)
	if err != nil {
		return err
	}
	if err := st.Comment(ctx, ref.repo, ref.number, comment); err != nil {
		return err
	}

	// A stage that did not produce a trustworthy report is a human's problem.
	if cls.Outcome != envelope.OutcomeOK {
		if err := c.stampClassification(ctx, st, ref, cls, labels.RecommendationManual); err != nil {
			return err
		}
		return c.handToHuman(ctx, st, ref,
			fmt.Sprintf("Classification did not complete (%s). Triage this finding manually.", cls.Outcome))
	}

	recommendation := recommendationOf(cls.Recommendation)
	if err := c.stampClassification(ctx, st, ref, cls, recommendation); err != nil {
		return err
	}

	switch {
	case recommendation == labels.RecommendationIgnore:
		return c.dismiss(ctx, st, ref, cls)
	case recommendation == labels.RecommendationManual:
		return c.handToHuman(ctx, st, ref,
			"The classifier recommends human remediation for this finding.")
	case cls.AwaitApproval:
		return c.holdForApproval(ctx, st, ref,
			"A better fix exists but would require breaking changes to external callers. "+
				"The backwards-compatible remediation was not attempted automatically.")
	case !cls.WillRemediate:
		return c.holdForApproval(ctx, st, ref, fmt.Sprintf(
			"Remediation confidence %.2f is below the automation threshold.", cls.Confidence))
	}
	// WillRemediate: the same pod is already remediating; its event follows.
	return nil
}

// stampClassification writes the classification labels (state, severity,
// priority, recommendation, confidence, budgets, accounting).
func (c *Controller) stampClassification(ctx context.Context, st Stores, ref issueRef,
	cls *envelope.Classification, recommendation labels.Recommendation) error {
	confidence := cls.Confidence
	next := labels.Set{
		Finding:        labels.StateClassified,
		Severity:       labels.Level(cls.Severity),
		Priority:       labels.Level(cls.Priority),
		Recommendation: recommendation,
		Confidence:     &confidence,
		Classifier:     cls.Harness,
		TokenBudget:    cls.TokenBudget,
		MaxTurns:       cls.MaxTurns,
		Classification: usageLabels(cls.Stage),
	}
	add := next.Render()
	if err := st.AddLabels(ctx, ref.repo, ref.number, add); err != nil {
		return err
	}
	return st.RemoveLabel(ctx, ref.repo, ref.number,
		labels.Name(labels.KeyFinding, string(labels.StateClassifying)))
}

// dismiss closes out a false positive: every accumulated alert is dismissed
// in GHAS and the issue is closed — DESIGN step 6, "if false positive".
func (c *Controller) dismiss(ctx context.Context, st Stores, ref issueRef, cls *envelope.Classification) error {
	issue, err := c.issue(ctx, st, ref)
	if err != nil {
		return err
	}
	manifest, err := templates.ParseManifest(issue.Body)
	if err != nil {
		return fmt.Errorf("%s: %w", ref, err)
	}
	comment := fmt.Sprintf("Dismissed as a false positive by patchy (confidence %.2f); see %s#%d.",
		cls.Confidence, ref.repo, ref.number)
	for _, alert := range manifest.Alerts {
		if err := st.DismissAlert(ctx, ref.repo, alert.Number, dismissReason, comment); err != nil {
			return fmt.Errorf("%s: dismiss alert %d: %w", ref, alert.Number, err)
		}
	}
	if err := st.Close(ctx, ref.repo, ref.number); err != nil {
		return err
	}
	c.log.LogAttrs(ctx, slog.LevelInfo, "finding dismissed as false positive",
		slog.String("issue", ref.String()), slog.Int("alerts", len(manifest.Alerts)))
	return nil
}

// handToHuman assigns the issue to its owners with an explanation.
func (c *Controller) handToHuman(ctx context.Context, st Stores, ref issueRef, reason string) error {
	if err := st.Comment(ctx, ref.repo, ref.number, reason); err != nil {
		return err
	}
	return c.assignOwners(ctx, st, ref)
}

// holdForApproval assigns the issue and explains the /approve escape hatch —
// DESIGN step 6's low-confidence and breaking-change paths.
func (c *Controller) holdForApproval(ctx context.Context, st Stores, ref issueRef, reason string) error {
	comment, err := templates.ApproveComment(reason)
	if err != nil {
		return err
	}
	if err := st.Comment(ctx, ref.repo, ref.number, comment); err != nil {
		return err
	}
	return c.assignOwners(ctx, st, ref)
}

// applyRemediation pushes the changeset and opens the pull request —
// DESIGN step 8.
func (c *Controller) applyRemediation(ctx context.Context, st Stores, ref issueRef,
	rem *envelope.Remediation) error {
	report := rem.ReportMarkdown
	if report == "" {
		report = fmt.Sprintf("_Remediation did not produce a report (%s): %s_", rem.Outcome, rem.Detail)
	}
	comment, err := templates.ReportComment(templates.RemediationReportHeading, report)
	if err != nil {
		return err
	}
	if err := st.Comment(ctx, ref.repo, ref.number, comment); err != nil {
		return err
	}

	stamp := labels.Set{
		Remediator:  rem.Harness,
		Remediation: usageLabels(rem.Stage),
	}
	if err := st.AddLabels(ctx, ref.repo, ref.number, stamp.Render()); err != nil {
		return err
	}

	if !rem.Success {
		detail := rem.Detail
		if detail == "" {
			detail = "the agent could not remediate this finding safely"
		}
		return c.attempted(ctx, st, ref, fmt.Sprintf("Automated remediation did not succeed (%s).", detail))
	}

	pr, err := c.openPR(ctx, st, ref, rem)
	if err != nil {
		return err
	}
	if err := c.setFindingLabel(ctx, st, ref, labels.StateInReview); err != nil {
		return err
	}
	c.log.LogAttrs(ctx, slog.LevelInfo, "remediation pull request opened",
		slog.String("issue", ref.String()), slog.Int("pr", pr.Number), slog.String("url", pr.HTMLURL))
	return nil
}

// openPR pushes the agent's branch from its changeset and opens the PR.
func (c *Controller) openPR(ctx context.Context, st Stores, ref issueRef,
	rem *envelope.Remediation) (*ghclient.PR, error) {
	if rem.Changeset == nil {
		return nil, fmt.Errorf("%s: remediation succeeded without a changeset", ref)
	}
	token, err := c.clients.PushToken(ctx, ref.repo)
	if err != nil {
		return nil, err
	}
	if err := c.pusher.Push(ctx, ref.repo, token, rem.Branch, rem.Changeset); err != nil {
		return nil, fmt.Errorf("%s: push remediation branch: %w", ref, err)
	}

	base, err := st.DefaultBranch(ctx, ref.repo)
	if err != nil {
		return nil, err
	}
	issue, err := c.issue(ctx, st, ref)
	if err != nil {
		return nil, err
	}
	body, err := templates.PRBody(ref.number, rem.ReportMarkdown)
	if err != nil {
		return nil, err
	}
	return st.CreatePR(ctx, ref.repo, ghclient.PRRequest{
		Title: fmt.Sprintf("fix(security): %s", issue.Title),
		Head:  rem.Branch,
		Base:  base,
		Body:  body,
	})
}

// attempted marks a finding the pipeline tried and could not fix, and hands
// it to a human.
func (c *Controller) attempted(ctx context.Context, st Stores, ref issueRef, reason string) error {
	if err := st.AddLabels(ctx, ref.repo, ref.number, []string{
		labels.Name(labels.KeyFinding, string(labels.StateAttempted)),
		labels.Name(labels.KeyRecommendation, string(labels.RecommendationManual)),
	}); err != nil {
		return err
	}
	for _, stale := range []labels.State{labels.StateClassifying, labels.StateClassified, labels.StateInReview} {
		if err := st.RemoveLabel(ctx, ref.repo, ref.number, labels.Name(labels.KeyFinding, string(stale))); err != nil {
			return err
		}
	}
	return c.handToHuman(ctx, st, ref, reason)
}

// assignOwners assigns the issue to the owners the enhancement recorded,
// falling back to leaving a warning when none are known.
func (c *Controller) assignOwners(ctx context.Context, st Stores, ref issueRef) error {
	comments, err := st.ListComments(ctx, ref.repo, ref.number)
	if err != nil {
		return err
	}
	var owners []string
	for _, cm := range comments {
		if enr, ok := templates.ParseEnrichment(cm.Body); ok && len(enr.Owners) > 0 {
			owners = enr.Owners
		}
	}
	if len(owners) == 0 {
		c.log.LogAttrs(ctx, slog.LevelWarn, "no owners known for issue; leaving unassigned",
			slog.String("issue", ref.String()))
		return st.Comment(ctx, ref.repo, ref.number,
			"_No repository owners are known for this finding; it could not be assigned automatically._")
	}
	return st.Assign(ctx, ref.repo, ref.number, owners)
}

// failAttempt records a failed agent attempt: retry while attempts remain,
// otherwise hand the finding to a human.
func (c *Controller) failAttempt(ctx context.Context, st Stores, ref issueRef, reason string) error {
	issue, err := c.issue(ctx, st, ref)
	if err != nil {
		return err
	}
	attempts := attemptsOf(issue) + 1
	if err := st.AddLabels(ctx, ref.repo, ref.number,
		[]string{labels.Name(labels.KeyAttempts, itoa(attempts))}); err != nil {
		return err
	}
	if prev := attempts - 1; prev > 0 {
		if err := st.RemoveLabel(ctx, ref.repo, ref.number,
			labels.Name(labels.KeyAttempts, itoa(prev))); err != nil {
			return err
		}
	}

	if attempts < c.cfg.MaxAttempts {
		c.log.LogAttrs(ctx, slog.LevelWarn, "agent attempt failed; will retry",
			slog.String("issue", ref.String()), slog.Int("attempts", attempts), slog.String("reason", reason))
		// Release the lease; the reconcile sweep picks it up again.
		return c.setState(ctx, st, ref, labels.StateClassifying, labels.StateContextEnhanced)
	}
	c.log.LogAttrs(ctx, slog.LevelError, "agent attempts exhausted",
		slog.String("issue", ref.String()), slog.Int("attempts", attempts), slog.String("reason", reason))
	return c.attempted(ctx, st, ref,
		fmt.Sprintf("Automated remediation failed %d times and will not be retried: %s", attempts, reason))
}

// setState moves the security-finding label, refusing illegal transitions.
func (c *Controller) setState(ctx context.Context, st Stores, ref issueRef, from, to labels.State) error {
	if !labels.CanTransition(from, to) {
		return fmt.Errorf("%s: illegal transition %s -> %s", ref, from, to)
	}
	if err := st.AddLabels(ctx, ref.repo, ref.number, []string{labels.Name(labels.KeyFinding, string(to))}); err != nil {
		return err
	}
	if from == "" || from == to {
		return nil
	}
	return st.RemoveLabel(ctx, ref.repo, ref.number, labels.Name(labels.KeyFinding, string(from)))
}

// setFindingLabel sets the state from whatever the issue currently carries.
func (c *Controller) setFindingLabel(ctx context.Context, st Stores, ref issueRef, to labels.State) error {
	issue, err := c.issue(ctx, st, ref)
	if err != nil {
		return err
	}
	return c.setState(ctx, st, ref, labels.Parse(issue.Labels).Finding, to)
}

// issue re-reads one issue by number (fresh state before every decision).
func (c *Controller) issue(ctx context.Context, st Stores, ref issueRef) (*ghclient.Issue, error) {
	open, err := st.ListOpen(ctx, ref.repo, nil)
	if err != nil {
		return nil, err
	}
	for _, is := range open {
		if is.Number == ref.number {
			return is, nil
		}
	}
	return nil, fmt.Errorf("%s: issue not found or closed", ref)
}

// recommendationOf converts the report's verdict to the label value. The two
// vocabularies are identical (ignore, remediate, manual); anything
// unrecognised falls back to manual, the route-to-humans safe default.
func recommendationOf(s string) labels.Recommendation {
	switch s {
	case "ignore":
		return labels.RecommendationIgnore
	case "remediate":
		return labels.RecommendationRemediate
	default:
		return labels.RecommendationManual
	}
}

func secondsToDuration(s float64) time.Duration {
	return time.Duration(s * float64(time.Second))
}
