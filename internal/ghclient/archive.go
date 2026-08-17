// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package ghclient

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/google/go-github/v90/github"
)

// Tarball streams the repository's tarball archive at ref (a SHA, branch, or
// tag). The caller must Close the reader. The archive is the tree only — no
// .git — which is exactly what the artifact server serves; agents synthesize
// their local git state from it.
func (c *Client) Tarball(ctx context.Context, repo Repo, ref string) (io.ReadCloser, error) {
	opts := &github.RepositoryContentGetOptions{Ref: ref}
	u, _, err := c.gh.Repositories.GetArchiveLink(ctx, repo.Owner, repo.Name, github.Tarball, opts, 3)
	if err != nil {
		return nil, fmt.Errorf("ghclient: archive link for %s@%s: %w", repo, ref, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("ghclient: archive request: %w", err)
	}
	// The client's transport injects auth — required on GHES; harmless on the
	// signed codeload URLs github.com hands out.
	resp, err := c.gh.Client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("ghclient: download archive for %s@%s: %w", repo, ref, err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("ghclient: download archive for %s@%s: status %d", repo, ref, resp.StatusCode)
	}
	return resp.Body, nil
}
