// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package ghclient

import (
	"context"
	"fmt"

	"github.com/google/go-github/v89/github"
)

// FindPRByHead returns the open pull request whose head is branch, or nil —
// the idempotency check before creating a remediation PR (a crash between
// push and status write must not open a duplicate).
func (c *Client) FindPRByHead(ctx context.Context, repo Repo, branch string) (*PR, error) {
	prs, _, err := c.gh.PullRequests.List(ctx, repo.Owner, repo.Name, &github.PullRequestListOptions{
		State:       "open",
		Head:        repo.Owner + ":" + branch,
		ListOptions: github.ListOptions{PerPage: 1},
	})
	if err != nil {
		return nil, fmt.Errorf("ghclient: find PR by head %s in %s: %w", branch, repo, err)
	}
	if len(prs) == 0 {
		return nil, nil
	}
	return &PR{Number: prs[0].GetNumber(), HTMLURL: prs[0].GetHTMLURL()}, nil
}
