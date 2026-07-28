// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package wizapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DefaultTokenURL is Wiz's commercial OAuth2 token endpoint; gov and other
// partitions override it on the Integration.
const DefaultTokenURL = "https://auth.app.wiz.io/oauth/token"

// audience is the OAuth2 audience Wiz service-account tokens are issued for.
const audience = "wiz-api"

// tokenSlack is how long before expiry a cached token is considered stale, so
// a request never departs with a token that dies in flight.
const tokenSlack = time.Minute

// Resolution reasons for a rejected issue, from Wiz's IssueResolutionReason
// vocabulary. Only the two the ignore verdict maps onto are declared.
const (
	// ResolutionFalsePositive marks the issue wrong on the facts.
	ResolutionFalsePositive = "FALSE_POSITIVE"
	// ResolutionWontFix accepts the finding but declines to act.
	ResolutionWontFix = "WONT_FIX"
)

// updateIssueMutation rejects an issue with a note. The patch shape is Wiz's
// UpdateIssuePatch; status REJECTED is Wiz's terminal "will not fix" state,
// the analogue of dismissing a code-scanning alert.
const updateIssueMutation = `mutation PatchyRejectIssue($id: ID!, $patch: UpdateIssuePatch) {
  updateIssue(input: {id: $id, patch: $patch}) {
    issue { id status }
  }
}`

// Options configure the client.
type Options struct {
	// Endpoint is the tenant GraphQL endpoint, e.g.
	// https://api.eu1.app.wiz.io/graphql.
	Endpoint string
	// TokenURL is the OAuth2 token endpoint; empty means DefaultTokenURL.
	TokenURL string
	// ClientID and ClientSecret are the Wiz service-account credentials.
	ClientID     string
	ClientSecret string
	// HTTPClient overrides the transport; nil means a 30-second-timeout
	// client.
	HTTPClient *http.Client
}

// Client calls the Wiz GraphQL API as one service account, caching its token
// until expiry.
type Client struct {
	opts Options
	http *http.Client

	mu      sync.Mutex
	token   string
	expires time.Time
}

// New builds a client. It makes no network call: the token is fetched lazily
// on first use, so construction cannot fail on a Wiz outage.
func New(o Options) (*Client, error) {
	if o.Endpoint == "" {
		return nil, errors.New("wizapi: endpoint is required")
	}
	if o.ClientID == "" || o.ClientSecret == "" {
		return nil, errors.New("wizapi: client credentials are required")
	}
	if o.TokenURL == "" {
		o.TokenURL = DefaultTokenURL
	}
	httpc := o.HTTPClient
	if httpc == nil {
		httpc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{opts: o, http: httpc}, nil
}

// RejectIssue moves an issue to REJECTED with a resolution reason and note.
func (c *Client) RejectIssue(ctx context.Context, issueID, reason, note string) error {
	vars := map[string]any{
		"id": issueID,
		"patch": map[string]any{
			"status":           "REJECTED",
			"resolutionReason": reason,
			"note":             note,
		},
	}
	return c.mutate(ctx, updateIssueMutation, vars)
}

// mutate posts one GraphQL operation and surfaces its errors.
func (c *Client) mutate(ctx context.Context, query string, variables map[string]any) error {
	token, err := c.accessToken(ctx)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return fmt.Errorf("wizapi: marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.opts.Endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("wizapi: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("wizapi: post mutation: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("wizapi: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("wizapi: mutation returned %s: %s", resp.Status, summarize(payload))
	}
	var out struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(payload, &out); err != nil {
		return fmt.Errorf("wizapi: decode response: %w", err)
	}
	if len(out.Errors) > 0 {
		msgs := make([]string, 0, len(out.Errors))
		for _, e := range out.Errors {
			msgs = append(msgs, e.Message)
		}
		return fmt.Errorf("wizapi: mutation failed: %s", strings.Join(msgs, "; "))
	}
	return nil
}

// accessToken returns the cached token, fetching a fresh one when absent or
// near expiry.
func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.expires.Add(-tokenSlack)) {
		return c.token, nil
	}
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"audience":      {audience},
		"client_id":     {c.opts.ClientID},
		"client_secret": {c.opts.ClientSecret},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.opts.TokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("wizapi: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("wizapi: fetch token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("wizapi: read token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("wizapi: token endpoint returned %s: %s", resp.Status, summarize(payload))
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(payload, &tok); err != nil {
		return "", fmt.Errorf("wizapi: decode token response: %w", err)
	}
	if tok.AccessToken == "" {
		return "", errors.New("wizapi: token endpoint returned no access_token")
	}
	c.token = tok.AccessToken
	c.expires = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	return c.token, nil
}

// summarize trims an error body for a log-safe message.
func summarize(body []byte) string {
	s := strings.TrimSpace(string(body))
	if len(s) > 256 {
		s = s[:256] + "..."
	}
	return s
}
