// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package ghclient

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/go-github/v90/github"
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

// Option adjusts how a Client is built.
type Option func(*settings)

// settings collects the optional client knobs.
type settings struct {
	proxyURL string
}

// WithProxy routes all of the client's traffic through the HTTP/HTTPS forward
// proxy at rawURL, overriding the process proxy environment. Empty is a no-op
// (the environment applies).
func WithProxy(rawURL string) Option {
	return func(s *settings) { s.proxyURL = rawURL }
}

// parseProxy parses a proxy URL, requiring an http or https scheme and a
// host. Empty returns nil — the environment applies.
func parseProxy(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("ghclient: proxy url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("ghclient: proxy url %q: scheme must be http or https", raw)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("ghclient: proxy url %q: missing host", raw)
	}
	return u, nil
}

// ValidateProxy checks rawURL is a usable proxy URL ("" is valid: no proxy).
func ValidateProxy(rawURL string) error {
	_, err := parseProxy(rawURL)
	return err
}

// NewToken returns a Client authenticated with a personal access token —
// the dev-mode fallback. baseURL "" means api.github.com.
func NewToken(token, baseURL string, opts ...Option) (*Client, error) {
	var s settings
	for _, opt := range opts {
		opt(&s)
	}
	proxy, err := parseProxy(s.proxyURL)
	if err != nil {
		return nil, err
	}
	gh, err := newGitHub(newRetryTransport(proxy), baseURL, github.WithAuthToken(token))
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
