// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package artifact

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Client talks to the store's internal blob endpoint
// (PUT|HEAD /internal/blobs/{digest}) from another process —
// evaluation-controller checking a workspace before launch or streaming an
// upload through to the cache.
type Client struct {
	// BaseURL of the internal endpoint, e.g.
	// http://patchy-source-controller.patchy.svc.cluster.local:9791.
	BaseURL string
	// Token is the optional shared bearer token.
	Token string
	// HTTPClient defaults to http.DefaultClient.
	HTTPClient *http.Client
}

func (c *Client) do(req *http.Request) (*http.Response, error) {
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	hc := c.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	return hc.Do(req)
}

func (c *Client) blobURL(digest string) string {
	return strings.TrimSuffix(c.BaseURL, "/") + "/internal/blobs/" + digest
}

// Stat reports whether the blob is cached.
func (c *Client) Stat(ctx context.Context, digest string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, c.blobURL(digest), nil)
	if err != nil {
		return false, fmt.Errorf("artifact: stat blob: %w", err)
	}
	resp, err := c.do(req)
	if err != nil {
		return false, fmt.Errorf("artifact: stat blob %s: %w", digest, err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("artifact: stat blob %s: unexpected status %d", digest, resp.StatusCode)
	}
}

// Put streams the tarball to the cache under its digest. The server verifies
// the digest; a mismatch surfaces as ErrDigestMismatch, an oversize upload as
// ErrBlobTooLarge.
func (c *Client) Put(ctx context.Context, digest string, r io.Reader, size int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.blobURL(digest), r)
	if err != nil {
		return fmt.Errorf("artifact: put blob: %w", err)
	}
	if size > 0 {
		req.ContentLength = size
	}
	resp, err := c.do(req)
	if err != nil {
		return fmt.Errorf("artifact: put blob %s: %w", digest, err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusCreated, http.StatusOK:
		return nil
	case http.StatusUnprocessableEntity:
		return ErrDigestMismatch
	case http.StatusRequestEntityTooLarge:
		return ErrBlobTooLarge
	default:
		return fmt.Errorf("artifact: put blob %s: unexpected status %d", digest, resp.StatusCode)
	}
}
