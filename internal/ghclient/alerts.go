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

// OpenAlert reopens a code-scanning alert (undoes a dismissal). Only
// dismissed alerts can be reopened; GitHub rejects the transition for
// fixed alerts.
func (c *Client) OpenAlert(ctx context.Context, repo Repo, number int) error {
	state := &github.CodeScanningAlertState{State: "open"}
	if _, _, err := c.gh.CodeScanning.UpdateAlert(ctx, repo.Owner, repo.Name, int64(number), state); err != nil {
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
