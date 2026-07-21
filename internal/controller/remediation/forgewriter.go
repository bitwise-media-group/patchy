// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package remediation

import (
	"context"
	"fmt"

	"github.com/bitwise-media-group/patchy/internal/envelope"
	"github.com/bitwise-media-group/patchy/internal/forge"
	"github.com/bitwise-media-group/patchy/internal/ghclient"
	"github.com/bitwise-media-group/patchy/internal/ghpush"
)

// forgeWriter is the production ForgeWriter: resolve the Forge covering the
// repository through the shared internal/forge seam, mint a per-operation
// scoped write token for the push, and drive the PR through the same
// installation client. This controller is the only holder of forge write
// scope.
type forgeWriter struct {
	forges *forge.Store
}

// NewForgeWriter builds the production ForgeWriter over the shared forge
// store.
func NewForgeWriter(forges *forge.Store) ForgeWriter {
	return &forgeWriter{forges: forges}
}

// Push implements ForgeWriter. The Git Data pusher targets the resolved
// Forge's baseURL — the same endpoint every other call on that Forge uses —
// so a GHES or fake forge is honoured on the write path too.
func (w *forgeWriter) Push(ctx context.Context, namespace, repoURL, branch string, cs *envelope.Changeset) error {
	res, err := w.forges.Resolve(ctx, namespace, repoURL)
	if err != nil {
		return err
	}
	token, _, err := w.forges.Token(ctx, res, forge.ScopeWrite)
	if err != nil {
		return fmt.Errorf("mint push token: %w", err)
	}
	return ghpush.New(res.Forge.Spec.BaseURL).Push(ctx, res.Repo, token, branch, cs)
}

// EnsurePR implements ForgeWriter: find-by-head first so a crash between
// push and status write never opens a duplicate.
func (w *forgeWriter) EnsurePR(
	ctx context.Context, namespace, repoURL, branch, title, body string,
) (int64, string, error) {
	res, err := w.forges.Resolve(ctx, namespace, repoURL)
	if err != nil {
		return 0, "", err
	}
	gh, err := w.forges.Client(ctx, res)
	if err != nil {
		return 0, "", err
	}
	if pr, err := gh.FindPRByHead(ctx, res.Repo, branch); err == nil && pr != nil {
		return int64(pr.Number), pr.HTMLURL, nil
	}
	base, err := gh.DefaultBranch(ctx, res.Repo)
	if err != nil {
		return 0, "", err
	}
	pr, err := gh.CreatePR(ctx, res.Repo, ghclient.PRRequest{
		Title: title, Head: branch, Base: base, Body: body,
	})
	if err != nil {
		return 0, "", err
	}
	return int64(pr.Number), pr.HTMLURL, nil
}
