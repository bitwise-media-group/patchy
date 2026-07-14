// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package ghclient

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"

	"github.com/google/go-github/v89/github"
)

// CommitFile is one file created or modified by a BranchPush.
type CommitFile struct {
	Path string
	// Mode is the git file mode: "100644", "100755", or "120000".
	Mode string
	// Content is the raw blob; for a symlink ("120000"), the link target.
	Content []byte
}

// BranchPush describes one commit to create on top of BaseSHA and the
// branch to point at it.
type BranchPush struct {
	Branch  string
	BaseSHA string
	Message string
	Files   []CommitFile
	Deletes []string
}

// PushBranch creates one commit on top of req.BaseSHA carrying req's file
// changes and points refs/heads/<Branch> at it, force-updating an existing
// branch so a retried event re-applies cleanly. It is the git-less push:
// blob → tree → commit → ref through the Git Data API.
func (c *Client) PushBranch(ctx context.Context, repo Repo, req BranchPush) error {
	entries := make([]*github.TreeEntry, 0, len(req.Files)+len(req.Deletes))
	for _, f := range req.Files {
		// Blobs go up base64-encoded, so binary content is safe.
		blob, _, err := c.gh.Git.CreateBlob(ctx, repo.Owner, repo.Name, github.Blob{
			Content:  new(base64.StdEncoding.EncodeToString(f.Content)),
			Encoding: new("base64"),
		})
		if err != nil {
			return fmt.Errorf("ghclient: create blob %s in %s: %w", f.Path, repo, err)
		}
		entries = append(entries, &github.TreeEntry{
			Path: new(f.Path), Mode: new(f.Mode), Type: new("blob"), SHA: blob.SHA,
		})
	}
	for _, path := range req.Deletes {
		// SHA and Content both nil serialize as "sha": null — a deletion.
		entries = append(entries, &github.TreeEntry{Path: new(path), Mode: new("100644")})
	}

	tree, _, err := c.gh.Git.CreateTree(ctx, repo.Owner, repo.Name, req.BaseSHA, entries)
	if err != nil {
		return fmt.Errorf("ghclient: create tree in %s: %w", repo, err)
	}
	// Author/committer are omitted: GitHub fills them from the token identity
	// (the App installation → patchy[bot]).
	commit, _, err := c.gh.Git.CreateCommit(ctx, repo.Owner, repo.Name, github.Commit{
		Message: new(req.Message),
		Tree:    &github.Tree{SHA: tree.SHA},
		Parents: []*github.Commit{{SHA: new(req.BaseSHA)}},
	}, nil)
	if err != nil {
		return fmt.Errorf("ghclient: create commit in %s: %w", repo, err)
	}

	_, resp, err := c.gh.Git.CreateRef(ctx, repo.Owner, repo.Name, github.CreateRef{
		Ref: "refs/heads/" + req.Branch,
		SHA: commit.GetSHA(),
	})
	if err != nil && resp != nil && resp.StatusCode == http.StatusUnprocessableEntity {
		// The branch already exists — a retried event; move it.
		_, _, err = c.gh.Git.UpdateRef(ctx, repo.Owner, repo.Name, "heads/"+req.Branch,
			github.UpdateRef{SHA: commit.GetSHA(), Force: new(true)})
	}
	if err != nil {
		return fmt.Errorf("ghclient: point %s at %s in %s: %w", req.Branch, commit.GetSHA(), repo, err)
	}
	return nil
}
