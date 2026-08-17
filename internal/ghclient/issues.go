// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package ghclient

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/go-github/v90/github"
)

// listPageSize is the per-page size for paginated list calls.
const listPageSize = 100

// ListOpen returns every open issue in repo carrying all of labels,
// following pagination. Pull requests (which the issues API also returns)
// are skipped.
func (c *Client) ListOpen(ctx context.Context, repo Repo, labels []string) ([]*Issue, error) {
	opts := &github.IssueListByRepoOptions{
		State:       "open",
		Labels:      labels,
		ListOptions: github.ListOptions{PerPage: listPageSize},
	}
	var out []*Issue
	for {
		page, resp, err := c.gh.Issues.ListByRepo(ctx, repo.Owner, repo.Name, opts)
		if err != nil {
			return nil, fmt.Errorf("ghclient: list open issues in %s: %w", repo, err)
		}
		for _, is := range page {
			if is.IsPullRequest() {
				continue
			}
			issue := issueFromGitHub(is)
			issue.Repo = repo
			out = append(out, issue)
		}
		if resp.NextPage == 0 {
			return out, nil
		}
		// ListByRepo options embed both cursor and offset pagination;
		// name the offset one to disambiguate.
		opts.ListOptions.Page = resp.NextPage
	}
}

// Create opens a new issue.
func (c *Client) Create(ctx context.Context, repo Repo, req IssueRequest) (*Issue, error) {
	body := github.CreateIssueRequest{
		Title: req.Title,
		Body:  new(req.Body),
	}
	if len(req.Labels) > 0 {
		body.Labels = req.Labels
	}
	is, _, err := c.gh.Issues.Create(ctx, repo.Owner, repo.Name, body)
	if err != nil {
		return nil, fmt.Errorf("ghclient: create issue in %s: %w", repo, err)
	}
	return issueFromGitHub(is), nil
}

// Comment adds a comment to the issue.
func (c *Client) Comment(ctx context.Context, repo Repo, number int, body string) error {
	comment := &github.IssueComment{Body: new(body)}
	if _, _, err := c.gh.Issues.CreateComment(ctx, repo.Owner, repo.Name, number, comment); err != nil {
		return fmt.Errorf("ghclient: comment on %s#%d: %w", repo, number, err)
	}
	return nil
}

// EditComment replaces the body of an existing issue comment.
func (c *Client) EditComment(ctx context.Context, repo Repo, commentID int64, body string) error {
	comment := &github.IssueComment{Body: new(body)}
	if _, _, err := c.gh.Issues.EditComment(ctx, repo.Owner, repo.Name, commentID, comment); err != nil {
		return fmt.Errorf("ghclient: edit comment %d on %s: %w", commentID, repo, err)
	}
	return nil
}

// ListComments returns every comment on the issue, following pagination.
func (c *Client) ListComments(ctx context.Context, repo Repo, number int) ([]*Comment, error) {
	opts := &github.IssueListCommentsOptions{ListOptions: github.ListOptions{PerPage: listPageSize}}
	var out []*Comment
	for {
		page, resp, err := c.gh.Issues.ListComments(ctx, repo.Owner, repo.Name, number, opts)
		if err != nil {
			return nil, fmt.Errorf("ghclient: list comments on %s#%d: %w", repo, number, err)
		}
		for _, gc := range page {
			out = append(out, &Comment{
				ID:                gc.GetID(),
				Body:              gc.GetBody(),
				UserLogin:         gc.GetUser().GetLogin(),
				AuthorAssociation: gc.GetAuthorAssociation(),
			})
		}
		if resp.NextPage == 0 {
			return out, nil
		}
		opts.Page = resp.NextPage
	}
}

// EditBody replaces the issue body.
func (c *Client) EditBody(ctx context.Context, repo Repo, number int, body string) error {
	req := github.UpdateIssueRequest{Body: new(body)}
	if _, _, err := c.gh.Issues.Update(ctx, repo.Owner, repo.Name, number, req); err != nil {
		return fmt.Errorf("ghclient: edit body of %s#%d: %w", repo, number, err)
	}
	return nil
}

// AddLabels adds labels to the issue, creating any that do not exist.
func (c *Client) AddLabels(ctx context.Context, repo Repo, number int, add []string) error {
	if len(add) == 0 {
		return nil
	}
	if _, _, err := c.gh.Issues.AddLabelsToIssue(ctx, repo.Owner, repo.Name, number, add); err != nil {
		return fmt.Errorf("ghclient: add labels to %s#%d: %w", repo, number, err)
	}
	return nil
}

// RemoveLabel removes one label from the issue. A 404 — the label is
// already absent — is not an error, so removals are idempotent.
func (c *Client) RemoveLabel(ctx context.Context, repo Repo, number int, name string) error {
	resp, err := c.gh.Issues.RemoveLabelForIssue(ctx, repo.Owner, repo.Name, number, name)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			return nil
		}
		return fmt.Errorf("ghclient: remove label %q from %s#%d: %w", name, repo, number, err)
	}
	return nil
}

// Assign adds assignees to the issue.
func (c *Client) Assign(ctx context.Context, repo Repo, number int, logins []string) error {
	if len(logins) == 0 {
		return nil
	}
	if _, _, err := c.gh.Issues.AddAssignees(ctx, repo.Owner, repo.Name, number, logins); err != nil {
		return fmt.Errorf("ghclient: assign %s#%d: %w", repo, number, err)
	}
	return nil
}

// Close closes the issue.
func (c *Client) Close(ctx context.Context, repo Repo, number int) error {
	req := github.UpdateIssueRequest{State: new("closed")}
	if _, _, err := c.gh.Issues.Update(ctx, repo.Owner, repo.Name, number, req); err != nil {
		return fmt.Errorf("ghclient: close %s#%d: %w", repo, number, err)
	}
	return nil
}

// issueFromGitHub maps a go-github issue onto patchy's thin Issue.
func issueFromGitHub(is *github.Issue) *Issue {
	out := &Issue{
		Repo:      repoFromURL(is.GetRepositoryURL()),
		Number:    is.GetNumber(),
		Title:     is.GetTitle(),
		Body:      is.GetBody(),
		State:     is.GetState(),
		CreatedAt: is.GetCreatedAt().Time,
	}
	for _, l := range is.Labels {
		out.Labels = append(out.Labels, l.GetName())
	}
	for _, a := range is.Assignees {
		out.Assignees = append(out.Assignees, a.GetLogin())
	}
	return out
}

// repoFromURL recovers owner/name from an API repository URL
// (".../repos/{owner}/{name}"); a URL of any other shape yields a zero Repo.
func repoFromURL(url string) Repo {
	parts := strings.Split(url, "/")
	for i, p := range parts {
		if p == "repos" && i+2 < len(parts) {
			return Repo{Owner: parts[i+1], Name: parts[i+2]}
		}
	}
	return Repo{}
}
