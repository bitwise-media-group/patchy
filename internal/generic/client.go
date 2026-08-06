// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package generic

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	pkggeneric "github.com/bitwise-media-group/patchy/pkg/generic"
	"github.com/bitwise-media-group/patchy/pkg/source"
)

// defaultTimeout bounds an outbound call when the Integration sets none; it
// matches the CRD default.
const defaultTimeout = 60 * time.Second

// maxResponseBody caps how much of a response is read, so a misbehaving
// endpoint cannot balloon memory.
const maxResponseBody = 1 << 20

// Sign computes the SignatureHeader value for a body: "sha256=<hex>" of the
// HMAC-SHA256 under the integration's shared secret. Exported because both
// sides of the contract need it — patchy signs outbound calls here, and
// test/replay tooling signs inbound deliveries the same way.
func Sign(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// ClientOptions configure a Client.
type ClientOptions struct {
	// URL the calls POST to.
	URL string
	// Secret signs each request body.
	Secret []byte
	// Timeout bounds each call; zero means defaultTimeout.
	Timeout time.Duration
	// HTTPClient overrides the transport in tests; nil means a default
	// client (the per-call timeout comes from the request context).
	HTTPClient *http.Client
}

// Client is the signed outbound HTTP client behind both generic exchanges
// patchy initiates: verdict write-back and enhancement.
type Client struct {
	url     string
	secret  []byte
	timeout time.Duration
	http    *http.Client
}

// NewClient builds a Client. Construction makes no network call, so it
// cannot fail on an endpoint outage — only on missing configuration.
func NewClient(o ClientOptions) (*Client, error) {
	if o.URL == "" {
		return nil, errors.New("generic: an endpoint url is required")
	}
	if len(o.Secret) == 0 {
		return nil, errors.New("generic: a signing secret is required")
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	httpc := o.HTTPClient
	if httpc == nil {
		httpc = &http.Client{}
	}
	return &Client{url: o.URL, secret: o.Secret, timeout: timeout, http: httpc}, nil
}

// post signs and delivers one request body, returning the status and (capped)
// response body of a 2xx answer; any other status is an error.
func (c *Client) post(ctx context.Context, v any) (int, []byte, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return 0, nil, fmt.Errorf("generic: encode request: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, fmt.Errorf("generic: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(pkggeneric.SignatureHeader, Sign(c.secret, body))
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("generic: call %s: %w", c.url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		return 0, nil, fmt.Errorf("generic: read response from %s: %w", c.url, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return 0, nil, fmt.Errorf("generic: %s returned %s: %s", c.url, resp.Status, summarize(payload))
	}
	return resp.StatusCode, payload, nil
}

// Enhance delivers one enhancement request; (nil, nil) when the endpoint
// answers 204 or an empty body — nothing to contribute.
func (c *Client) Enhance(ctx context.Context, req pkggeneric.EnhanceRequest) (*pkggeneric.EnhanceResponse, error) {
	status, body, err := c.post(ctx, req)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNoContent || len(bytes.TrimSpace(body)) == 0 {
		return nil, nil
	}
	var out pkggeneric.EnhanceResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("generic: decode enhance response from %s: %w", c.url, err)
	}
	return &out, nil
}

// Resolver is the generic write-back: it tells the external process what the
// pipeline decided, over the same signed channel. Idempotency is the
// process's obligation — the contract says so, and patchy retries.
type Resolver struct {
	c           *Client
	integration string
}

var _ source.Resolver = (*Resolver)(nil)

// NewResolver builds the write-back for one integration.
func NewResolver(c *Client, integration string) *Resolver {
	return &Resolver{c: c, integration: integration}
}

// Resolve implements source.Resolver: one POST carrying every alert, any 2xx
// is success.
func (r *Resolver) Resolve(ctx context.Context, alerts []source.AlertRef, v source.Verdict) error {
	refs := make([]pkggeneric.AlertRef, 0, len(alerts))
	for _, a := range alerts {
		refs = append(refs, pkggeneric.AlertRef{ID: a.ID, URL: a.URL})
	}
	_, _, err := r.c.post(ctx, pkggeneric.ResolveRequest{
		Version:     pkggeneric.Version,
		Integration: r.integration,
		Alerts:      refs,
		Verdict:     pkggeneric.Verdict{Kind: string(v.Kind), Reason: v.Reason, Comment: v.Comment},
	})
	return err
}

// summarize trims a response body for an error message, so a huge or hostile
// answer cannot flood the log.
func summarize(body []byte) string {
	s := string(bytes.TrimSpace(body))
	if len(s) > 256 {
		return s[:256] + "..."
	}
	return s
}
