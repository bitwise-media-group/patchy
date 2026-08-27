// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package ghclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// limitNTimes answers rate-limit rejections for the first n requests
// (headers applied via set), then succeeds, counting every hit.
func limitNTimes(t *testing.T, n int32, set func(h http.Header)) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if body, err := io.ReadAll(r.Body); err != nil || (r.Method == http.MethodPost && string(body) != "hello") {
			t.Errorf("attempt %d body = %q (err %v), want %q", hits.Load()+1, body, err, "hello")
		}
		if hits.Add(1) <= n {
			set(w.Header())
			http.Error(w, "rate limited", http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func retryAfter(secs int) func(h http.Header) {
	return func(h http.Header) { h.Set("Retry-After", strconv.Itoa(secs)) }
}

func TestRetryTransport(t *testing.T) {
	tests := []struct {
		name      string
		limited   int32 // rejections before the fake succeeds
		set       func(h http.Header)
		wantCode  int
		wantHits  int32
		emptyBody bool
	}{
		{
			name:     "retries through two rate limits",
			limited:  2,
			set:      retryAfter(0),
			wantCode: http.StatusOK,
			wantHits: 3,
		},
		{
			name:     "gives up after max attempts",
			limited:  99,
			set:      retryAfter(0),
			wantCode: http.StatusForbidden,
			wantHits: maxAttempts,
		},
		{
			name:    "waits until the ratelimit reset epoch",
			limited: 1,
			set: func(h http.Header) {
				h.Set("X-Ratelimit-Remaining", "0")
				h.Set("X-Ratelimit-Reset", strconv.FormatInt(time.Now().Add(-time.Second).Unix(), 10))
			},
			wantCode: http.StatusOK,
			wantHits: 2,
		},
		{
			name:      "non-rate-limit 403 passes through",
			limited:   99,
			set:       func(http.Header) {},
			wantCode:  http.StatusForbidden,
			wantHits:  1,
			emptyBody: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, hits := limitNTimes(t, tt.limited, tt.set)
			client := &http.Client{Transport: newRetryTransport(nil)}

			var (
				resp *http.Response
				err  error
			)
			if tt.emptyBody {
				resp, err = client.Get(srv.URL)
			} else {
				// A replayable body (strings.Reader gives GetBody) that the
				// fake asserts on every attempt, proving the reset works.
				resp, err = client.Post(srv.URL, "text/plain", strings.NewReader("hello"))
			}
			if err != nil {
				t.Fatalf("request error = %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != tt.wantCode {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.wantCode)
			}
			if got := hits.Load(); got != tt.wantHits {
				t.Errorf("server hit %d times, want %d", got, tt.wantHits)
			}
		})
	}
}

func TestRetryTransportNonReplayableBody(t *testing.T) {
	srv, hits := limitNTimes(t, 99, retryAfter(0))
	client := &http.Client{Transport: newRetryTransport(nil)}

	// Wrapping the reader hides its type from http.NewRequest, so GetBody
	// stays nil and the transport must not retry.
	req, err := http.NewRequest(http.MethodPost, srv.URL, struct{ io.Reader }{strings.NewReader("hello")})
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request error = %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403 passed through", resp.StatusCode)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("server hit %d times, want 1 (no retry without GetBody)", got)
	}
}

func TestRetryTransportContextCancelled(t *testing.T) {
	srv, hits := limitNTimes(t, 99, retryAfter(30))
	client := &http.Client{Transport: newRetryTransport(nil)}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	start := time.Now()
	resp, err := client.Do(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("Do() error = nil, want context deadline exceeded")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Do() error = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("cancellation took %v, want the wait aborted promptly", elapsed)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("server hit %d times, want 1", got)
	}
}
