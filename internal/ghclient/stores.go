// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package ghclient

import "context"

// IssueStore is the issue surface the controllers use; *Client implements
// it, controller tests fake it.
type IssueStore interface {
	// ListOpen returns every open issue in repo carrying all of labels.
	ListOpen(ctx context.Context, repo Repo, labels []string) ([]*Issue, error)
	// Create opens a new issue.
	Create(ctx context.Context, repo Repo, req IssueRequest) (*Issue, error)
	// Comment adds a comment to the issue.
	Comment(ctx context.Context, repo Repo, number int, body string) error
	// ListComments returns every comment on the issue, oldest first.
	ListComments(ctx context.Context, repo Repo, number int) ([]*Comment, error)
	// EditBody replaces the issue body.
	EditBody(ctx context.Context, repo Repo, number int, body string) error
	// AddLabels adds labels to the issue, creating them as needed.
	AddLabels(ctx context.Context, repo Repo, number int, add []string) error
	// RemoveLabel removes one label; a label already absent is not an error.
	RemoveLabel(ctx context.Context, repo Repo, number int, name string) error
	// Assign adds assignees to the issue.
	Assign(ctx context.Context, repo Repo, number int, logins []string) error
	// Close closes the issue.
	Close(ctx context.Context, repo Repo, number int) error
}

// AlertStore is the code-scanning surface.
type AlertStore interface {
	// GetAlert fetches one code-scanning alert.
	GetAlert(ctx context.Context, repo Repo, number int) (*Alert, error)
	// DismissAlert dismisses an alert; reason must be one of GitHub's
	// "false positive", "won't fix", or "used in tests".
	DismissAlert(ctx context.Context, repo Repo, number int, reason, comment string) error
}

// RepoStore is the repository/PR/search surface.
type RepoStore interface {
	// DefaultBranch returns the repository's default branch name.
	DefaultBranch(ctx context.Context, repo Repo) (string, error)
	// HeadSHA resolves the current commit SHA of refs/heads/<branch>.
	HeadSHA(ctx context.Context, repo Repo, branch string) (string, error)
	// CreatePR opens a pull request.
	CreatePR(ctx context.Context, repo Repo, req PRRequest) (*PR, error)
	// SearchIssues runs an issue search query (cross-repo reconcile
	// queries) and returns every matching issue.
	SearchIssues(ctx context.Context, query string) ([]*Issue, error)
}

var (
	_ IssueStore = (*Client)(nil)
	_ AlertStore = (*Client)(nil)
	_ RepoStore  = (*Client)(nil)
)
