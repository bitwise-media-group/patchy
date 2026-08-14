// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package broker

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// vertexScope is the OAuth scope the Vertex AI prediction API accepts.
const vertexScope = "https://www.googleapis.com/auth/cloud-platform"

// Vertex is the GCP Vertex AI route: attach an OAuth bearer from Application
// Default Credentials (GKE Workload Identity in-cluster) and forward. The
// token source caches and refreshes; each request reads the current token.
func Vertex(ctx context.Context, target *url.URL) (Upstream, error) {
	ts, err := google.DefaultTokenSource(ctx, vertexScope)
	if err != nil {
		return Upstream{}, fmt.Errorf("broker: google default credentials: %w", err)
	}
	ts = oauth2.ReuseTokenSource(nil, ts)
	return Upstream{
		Target: target,
		Credential: func(_ context.Context, req *http.Request) error {
			tok, err := ts.Token()
			if err != nil {
				return fmt.Errorf("resolve Google token: %w", err)
			}
			tok.SetAuthHeader(req)
			return nil
		},
		Ready: func(context.Context) error {
			_, err := ts.Token()
			return err
		},
	}, nil
}
