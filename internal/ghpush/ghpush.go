// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package ghpush

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/bitwise-media-group/patchy/internal/envelope"
	"github.com/bitwise-media-group/patchy/internal/ghclient"
)

// Pusher pushes agent changesets through the GitHub API.
type Pusher struct {
	baseURL string
}

// New builds a Pusher. baseURL overrides the GitHub API endpoint (GHES,
// tests); empty means github.com.
func New(baseURL string) *Pusher { return &Pusher{baseURL: baseURL} }

// Push creates one commit from the changeset on top of its base SHA and
// points branch at it. The short-lived write token lives only for this call
// and is never persisted.
func (p *Pusher) Push(ctx context.Context, repo ghclient.Repo, token, branch string,
	cs *envelope.Changeset) error {
	client, err := ghclient.NewToken(token, p.baseURL)
	if err != nil {
		return fmt.Errorf("ghpush: %w", err)
	}
	req := ghclient.BranchPush{
		Branch:  branch,
		BaseSHA: cs.BaseSHA,
		Message: cs.CommitMessage,
		Deletes: cs.Deletes,
	}
	for _, up := range cs.Upserts {
		content, err := base64.StdEncoding.DecodeString(up.ContentB64)
		if err != nil {
			return fmt.Errorf("ghpush: decode %s: %w", up.Path, err)
		}
		req.Files = append(req.Files, ghclient.CommitFile{Path: up.Path, Mode: up.Mode, Content: content})
	}
	return client.PushBranch(ctx, repo, req)
}
