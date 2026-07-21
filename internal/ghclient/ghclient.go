// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package ghclient

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/google/go-github/v89/github"
)

// Repo identifies a GitHub repository.
type Repo struct {
	Owner string
	Name  string
}

// String renders the repository as "owner/name".
func (r Repo) String() string { return r.Owner + "/" + r.Name }

// Client wraps the GitHub REST API with patchy's narrow, fake-able
// surface. Construct one with NewToken or App.Installation.
type Client struct {
	gh *github.Client
	// token is the personal access token a dev-mode client authenticates
	// with; empty for installation clients, whose credentials are minted
	// per request by the App transport.
	token string
}

// NewToken returns a Client authenticated with a personal access token —
// the dev-mode fallback. baseURL "" means api.github.com.
func NewToken(token, baseURL string) (*Client, error) {
	gh, err := newGitHub(newRetryTransport(), baseURL, github.WithAuthToken(token))
	if err != nil {
		return nil, fmt.Errorf("ghclient: token client: %w", err)
	}
	return &Client{gh: gh, token: token}, nil
}

// newGitHub builds a go-github client on transport, pointed at baseURL
// ("" means api.github.com; anything else goes through WithEnterpriseURLs,
// which appends /api/v3/ for non-api hosts — the GHES convention).
func newGitHub(transport http.RoundTripper, baseURL string,
	extra ...github.ClientOptionsFunc) (*github.Client, error) {
	opts := append([]github.ClientOptionsFunc{github.WithTransport(transport)}, extra...)
	if baseURL != "" {
		opts = append(opts, github.WithEnterpriseURLs(baseURL, baseURL))
	}
	return github.NewClient(opts...)
}

// apiRoot is gh's resolved base URL without the trailing slash — the form
// ghinstallation expects ("https://api.github.com", "https://ghes/api/v3").
func apiRoot(gh *github.Client) string {
	return strings.TrimSuffix(gh.BaseURL(), "/")
}
