// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package ghclient

import (
	"context"
	"fmt"

	"github.com/google/go-github/v89/github"
)

// GetAlert fetches one code-scanning alert.
func (c *Client) GetAlert(ctx context.Context, repo Repo, number int) (*Alert, error) {
	ga, _, err := c.gh.CodeScanning.GetAlert(ctx, repo.Owner, repo.Name, int64(number))
	if err != nil {
		return nil, fmt.Errorf("ghclient: get alert %s#%d: %w", repo, number, err)
	}
	return alertFromGitHub(ga), nil
}

// DismissAlert dismisses a code-scanning alert. reason must be one of
// GitHub's "false positive", "won't fix", or "used in tests".
func (c *Client) DismissAlert(ctx context.Context, repo Repo, number int, reason, comment string) error {
	state := &github.CodeScanningAlertState{
		State:            "dismissed",
		DismissedReason:  github.Ptr(reason),
		DismissedComment: github.Ptr(comment),
	}
	if _, _, err := c.gh.CodeScanning.UpdateAlert(ctx, repo.Owner, repo.Name, int64(number), state); err != nil {
		return fmt.Errorf("ghclient: dismiss alert %s#%d: %w", repo, number, err)
	}
	return nil
}

// alertStateDismissed is GitHub's state for an alert a triage decision
// closed — the only state a reopen is legal from.
const alertStateDismissed = "dismissed"

// OpenAlert reopens a code-scanning alert (undoes a dismissal). Only a
// dismissed alert can be reopened, so the current state is read first and
// anything else is success: an alert that is already open, fixed, or gone
// with the repository that held it has no dismissal left to undo.
// Reopening is therefore convergent rather than authoritative — the retried
// cleanup of a repository that was deleted or recreated since the dismissal
// still succeeds. GitHub rejects every illegal transition with the same
// opaque 400 ("There was an issue creating the request. Please try again."),
// which is why the state is read rather than sniffed out of the error.
func (c *Client) OpenAlert(ctx context.Context, repo Repo, number int) error {
	alert, _, err := c.gh.CodeScanning.GetAlert(ctx, repo.Owner, repo.Name, int64(number))
	if err != nil {
		if IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("ghclient: get alert %s#%d: %w", repo, number, err)
	}
	if alert.GetState() != alertStateDismissed {
		return nil
	}
	state := &github.CodeScanningAlertState{State: "open"}
	if _, _, err := c.gh.CodeScanning.UpdateAlert(ctx, repo.Owner, repo.Name, int64(number), state); err != nil {
		if IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("ghclient: open alert %s#%d: %w", repo, number, err)
	}
	return nil
}

// alertFromGitHub maps a go-github alert onto patchy's Alert: rule
// metadata and tags, security_severity_level falling back to the rule
// severity, and the most recent instance's commit, message, and location.
func alertFromGitHub(ga *github.Alert) *Alert {
	a := &Alert{
		Number:  ga.GetNumber(),
		State:   ga.GetState(),
		HTMLURL: ga.GetHTMLURL(),
	}
	if rule := ga.GetRule(); rule != nil {
		a.RuleID = rule.GetID()
		a.RuleName = rule.GetName()
		a.RuleDescription = rule.GetDescription()
		a.RuleHelp = rule.GetHelp()
		a.Tags = rule.Tags
		a.Severity = rule.GetSecuritySeverityLevel()
		if a.Severity == "" {
			a.Severity = rule.GetSeverity()
		}
	}
	if inst := ga.GetMostRecentInstance(); inst != nil {
		a.MostRecentSHA = inst.GetCommitSHA()
		a.Snippet = inst.GetMessage().GetText()
		if loc := inst.GetLocation(); loc != nil {
			a.Path = loc.GetPath()
			a.StartLine = loc.GetStartLine()
			a.EndLine = loc.GetEndLine()
		}
	}
	return a
}
