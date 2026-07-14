// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package ghclient

import (
	"context"
	"fmt"

	"github.com/google/go-github/v89/github"
)

// DefaultBranch returns the repository's default branch name.
func (c *Client) DefaultBranch(ctx context.Context, repo Repo) (string, error) {
	r, _, err := c.gh.Repositories.Get(ctx, repo.Owner, repo.Name)
	if err != nil {
		return "", fmt.Errorf("ghclient: get %s: %w", repo, err)
	}
	return r.GetDefaultBranch(), nil
}

// HeadSHA resolves the current commit SHA of refs/heads/<branch>.
func (c *Client) HeadSHA(ctx context.Context, repo Repo, branch string) (string, error) {
	ref, _, err := c.gh.Git.GetRef(ctx, repo.Owner, repo.Name, "heads/"+branch)
	if err != nil {
		return "", fmt.Errorf("ghclient: resolve %s head of %s: %w", branch, repo, err)
	}
	return ref.GetObject().GetSHA(), nil
}

// CreatePR opens a pull request.
func (c *Client) CreatePR(ctx context.Context, repo Repo, req PRRequest) (*PR, error) {
	pr, _, err := c.gh.PullRequests.Create(ctx, repo.Owner, repo.Name, &github.NewPullRequest{
		Title: new(req.Title),
		Head:  new(req.Head),
		Base:  new(req.Base),
		Body:  new(req.Body),
	})
	if err != nil {
		return nil, fmt.Errorf("ghclient: create PR in %s: %w", repo, err)
	}
	return &PR{Number: pr.GetNumber(), HTMLURL: pr.GetHTMLURL()}, nil
}

// SearchIssues runs an issue search query and returns every matching
// issue, following pagination.
func (c *Client) SearchIssues(ctx context.Context, query string) ([]*Issue, error) {
	opts := &github.SearchOptions{ListOptions: github.ListOptions{PerPage: listPageSize}}
	var out []*Issue
	for {
		res, resp, err := c.gh.Search.Issues(ctx, query, opts)
		if err != nil {
			return nil, fmt.Errorf("ghclient: search issues %q: %w", query, err)
		}
		for _, is := range res.Issues {
			out = append(out, issueFromGitHub(is))
		}
		if resp.NextPage == 0 {
			return out, nil
		}
		opts.Page = resp.NextPage
	}
}
