// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package ghclient

import (
	"context"
	"fmt"
)

// GetIssue fetches one issue: the projection reconciler's current-state
// read before diffing labels and body.
func (c *Client) GetIssue(ctx context.Context, repo Repo, number int) (*Issue, error) {
	is, _, err := c.gh.Issues.Get(ctx, repo.Owner, repo.Name, number)
	if err != nil {
		return nil, fmt.Errorf("ghclient: get issue %s#%d: %w", repo, number, err)
	}
	out := &Issue{
		Repo:      repo,
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
	return out, nil
}
