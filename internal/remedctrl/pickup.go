// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package remedctrl

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/bitwise-media-group/patchy/internal/envelope"
	"github.com/bitwise-media-group/patchy/internal/ghclient"
	"github.com/bitwise-media-group/patchy/internal/jobs"
	"github.com/bitwise-media-group/patchy/internal/labels"
	"github.com/bitwise-media-group/patchy/internal/templates"
)

// pickup leases the issue and runs the agent Job for it. The lease is the
// classifying label written before the Job exists: a crashed controller
// finds classifying issues with no live Job and reverts them (see
// reapOrphans).
func (c *Controller) pickup(ctx context.Context, st Stores, issue *ghclient.Issue, phase string) error {
	ref := issueRef{issue.Repo, issue.Number}
	set := labels.Parse(issue.Labels)

	if err := c.setState(ctx, st, ref, set.Finding, labels.StateClassifying); err != nil {
		return err
	}

	job, err := c.launch(ctx, st, issue, phase, set.Attempts)
	if err != nil {
		// The lease is held but no Job exists; revert now rather than
		// waiting for the orphan sweep.
		if rerr := c.setState(ctx, st, ref, labels.StateClassifying, labels.StateContextEnhanced); rerr != nil {
			return errors.Join(err, rerr)
		}
		return err
	}

	c.log.LogAttrs(ctx, slog.LevelInfo, "agent job created",
		slog.String("issue", ref.String()), slog.String("job", job), slog.String("phase", phase))

	// Follow the pod live so the classification event's side effects land
	// while the same pod continues into remediation. A broken stream is not
	// fatal: the completed Job's full log is re-read below, and events are
	// idempotent to re-apply.
	// An event is marked applied only once its side effects have landed, so
	// a failure during the live follow is retried from the completed Job's
	// full log rather than silently skipped.
	applied := make(map[envelope.Type]bool)
	apply := func(e envelope.Event) error {
		if applied[e.Type] {
			return nil
		}
		if err := c.applyEvent(ctx, st, ref, e); err != nil {
			return err
		}
		applied[e.Type] = true
		return nil
	}
	if err := c.runner.Follow(ctx, job, apply); err != nil {
		c.log.LogAttrs(ctx, slog.LevelWarn, "log follow failed; falling back to job result",
			slog.String("issue", ref.String()), slog.String("job", job), slog.Any("error", err))
	}

	events, err := c.runner.Result(ctx, job)
	if err != nil {
		return c.failAttempt(ctx, st, ref, fmt.Sprintf("could not read agent job output: %v", err))
	}
	var errs []error
	for _, e := range events {
		if err := apply(e); err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	if len(events) == 0 {
		return c.failAttempt(ctx, st, ref, "the agent produced no result")
	}
	return nil
}

// launch assembles the workspace inputs and creates the Job.
func (c *Controller) launch(ctx context.Context, st Stores, issue *ghclient.Issue,
	phase string, attempt int) (string, error) {
	branch, err := st.DefaultBranch(ctx, issue.Repo)
	if err != nil {
		return "", err
	}
	// Pin the Job to the branch head as of now: the clone, the agent's diff
	// base, and the pushed commit's parent are all this SHA, so the base
	// advancing mid-run just surfaces as a normal behind/conflicted PR — the
	// push itself never touches the default branch.
	baseSHA, err := st.HeadSHA(ctx, issue.Repo, branch)
	if err != nil {
		return "", err
	}
	token, err := c.clients.CloneToken(ctx, issue.Repo)
	if err != nil {
		return "", err
	}
	comments, err := st.ListComments(ctx, issue.Repo, issue.Number)
	if err != nil {
		return "", err
	}
	bodies := make([]string, 0, len(comments))
	for _, cm := range comments {
		bodies = append(bodies, cm.Body)
	}
	handoff, err := templates.RenderWorkspaceIssue(templates.WorkspaceIssue{
		Repo:     issue.Repo.String(),
		Number:   issue.Number,
		Title:    issue.Title,
		Body:     issue.Body,
		Comments: bodies,
	})
	if err != nil {
		return "", err
	}

	spec := jobs.Spec{
		Repo:          issue.Repo.String(),
		Issue:         issue.Number,
		Attempt:       attempt,
		Phase:         phase,
		CloneURL:      fmt.Sprintf("https://github.com/%s.git", issue.Repo),
		BaseSHA:       baseSHA,
		Token:         token,
		IssueMarkdown: handoff,
	}
	if phase == phaseRemediate {
		// The /approve re-run: hand the agent the classification the
		// pipeline already produced, recovered from its issue comment.
		report, ok := findClassificationReport(comments)
		if !ok {
			return "", errors.New("no classification report found on the issue")
		}
		spec.ClassificationMarkdown = report
	}
	return c.runner.Create(ctx, spec)
}

// applyEvent lands one envelope event's GitHub side effects.
func (c *Controller) applyEvent(ctx context.Context, st Stores, ref issueRef, e envelope.Event) error {
	switch e.Type {
	case envelope.TypeClassification:
		if e.Classification == nil {
			return fmt.Errorf("%s: classification event carries no payload", ref)
		}
		return c.applyClassification(ctx, st, ref, e.Classification)
	case envelope.TypeRemediation:
		if e.Remediation == nil {
			return fmt.Errorf("%s: remediation event carries no payload", ref)
		}
		return c.applyRemediation(ctx, st, ref, e.Remediation)
	case envelope.TypeFatal:
		return c.failAttempt(ctx, st, ref, e.Error)
	}
	return nil
}

// findClassificationReport recovers the classification report from the issue
// comments the controller posted — GitHub stays the only state store, so the
// /approve re-run re-reads what it needs from the issue itself.
func findClassificationReport(comments []*ghclient.Comment) (string, bool) {
	for i := len(comments) - 1; i >= 0; i-- {
		body := comments[i].Body
		if !strings.Contains(body, templates.ClassificationReportHeading) {
			continue
		}
		// The comment wraps the raw report; the report starts at its
		// frontmatter fence.
		if i := strings.Index(body, "---\n"); i >= 0 {
			return body[i:], true
		}
	}
	return "", false
}

// usageLabels projects an envelope stage's accounting onto the label set.
func usageLabels(st envelope.Stage) *labels.Usage {
	return &labels.Usage{
		InputTokens:  st.Usage.InputTokens,
		OutputTokens: st.Usage.OutputTokens,
		Turns:        st.NumTurns,
		CostUSD:      st.Usage.CostUSD,
		Session:      st.SessionID,
		Elapsed:      secondsToDuration(st.ElapsedSeconds),
	}
}

// attemptsOf reads the retry counter from an issue's labels.
func attemptsOf(issue *ghclient.Issue) int { return labels.Parse(issue.Labels).Attempts }

// itoa is strconv.Itoa, named for label-building readability.
func itoa(n int) string { return strconv.Itoa(n) }
