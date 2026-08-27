// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package ghclient

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Rate-limit retry policy: at most maxAttempts tries per request in total,
// never sleeping longer than maxWait before one retry.
const (
	maxAttempts = 3
	maxWait     = 60 * time.Second
)

// retryTransport retries requests that GitHub rejected for rate limiting —
// a 403/429 carrying Retry-After, or an exhausted x-ratelimit-remaining —
// after waiting out the advertised delay. Everything else passes through
// untouched. A request whose body cannot be replayed (non-nil Body without
// GetBody) is never retried.
type retryTransport struct {
	base http.RoundTripper
}

// newRetryTransport wraps a clone of http.DefaultTransport with the retry
// policy. A non-nil proxy routes every request through that forward proxy —
// the resource-level override; nil keeps the clone's ProxyFromEnvironment, so
// HTTPS_PROXY/HTTP_PROXY/NO_PROXY apply.
func newRetryTransport(proxy *url.URL) *retryTransport {
	base := http.DefaultTransport.(*http.Transport).Clone()
	if proxy != nil {
		base.Proxy = http.ProxyURL(proxy)
	}
	return &retryTransport{base: base}
}

// RoundTrip implements http.RoundTripper.
func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	for attempt := 1; ; attempt++ {
		resp, err := t.base.RoundTrip(req)
		if err != nil {
			return resp, err
		}
		wait, limited := rateLimitWait(resp)
		if !limited || attempt >= maxAttempts || !replayable(req) {
			return resp, nil
		}
		drain(resp)
		if err := sleep(req.Context(), wait); err != nil {
			return nil, err
		}
		if req.Body != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			req.Body = body
		}
	}
}

// rateLimitWait reports whether resp is a rate-limit rejection and how long
// to wait before retrying: the Retry-After seconds if present, otherwise
// until the x-ratelimit-reset epoch when the remaining budget is zero.
func rateLimitWait(resp *http.Response) (time.Duration, bool) {
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusTooManyRequests {
		return 0, false
	}
	if s := resp.Header.Get("Retry-After"); s != "" {
		if secs, err := strconv.Atoi(s); err == nil && secs >= 0 {
			return capWait(time.Duration(secs) * time.Second), true
		}
	}
	if resp.Header.Get("X-Ratelimit-Remaining") == "0" {
		if epoch, err := strconv.ParseInt(resp.Header.Get("X-Ratelimit-Reset"), 10, 64); err == nil {
			return capWait(time.Until(time.Unix(epoch, 0))), true
		}
	}
	return 0, false
}

// capWait clamps a wait into [0, maxWait].
func capWait(d time.Duration) time.Duration {
	return max(0, min(d, maxWait))
}

// replayable reports whether the request can be sent again: bodyless, or
// carrying a body that GetBody can recreate.
func replayable(req *http.Request) bool {
	return req.Body == nil || req.GetBody != nil
}

// sleep waits for d unless ctx ends first.
func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// drain discards and closes a response body so the connection is reusable
// for the retry.
func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}
