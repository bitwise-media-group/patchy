// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package webhookctrl

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bitwise-media-group/patchy/internal/webhook"
)

var secret = []byte("s3cret")

func sign(body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// capture records the last delivery an upstream received.
type capture struct {
	hits      atomic.Int64
	event     atomic.Value // string
	delivery  atomic.Value // string
	signature atomic.Value // string
	body      atomic.Value // string
}

func (c *capture) handler(status int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		c.hits.Add(1)
		c.event.Store(r.Header.Get("X-GitHub-Event"))
		c.delivery.Store(r.Header.Get("X-GitHub-Delivery"))
		c.signature.Store(r.Header.Get("X-Hub-Signature-256"))
		c.body.Store(string(body))
		w.WriteHeader(status)
	}
}

func TestHandleRoutesByEventType(t *testing.T) {
	var source, contextc, remed capture
	upSource := httptest.NewServer(source.handler(http.StatusAccepted))
	defer upSource.Close()
	upContext := httptest.NewServer(contextc.handler(http.StatusAccepted))
	defer upContext.Close()
	upRemed := httptest.NewServer(remed.handler(http.StatusNoContent))
	defer upRemed.Close()

	routes := map[string][]string{
		"code_scanning_alert": {upSource.URL + "/webhook"},
		"issues":              {upContext.URL + "/webhook"},
		"issue_comment":       {upRemed.URL + "/webhook"},
		"pull_request":        {upRemed.URL + "/webhook"},
	}
	f := New(Config{Secret: secret, Routes: routes}, slog.New(slog.DiscardHandler))

	tests := []struct {
		event string
		want  *capture
		rest  []*capture
	}{
		{event: "code_scanning_alert", want: &source, rest: []*capture{&contextc, &remed}},
		{event: "issues", want: &contextc, rest: []*capture{&source, &remed}},
		{event: "issue_comment", want: &remed, rest: []*capture{&source, &contextc}},
		{event: "pull_request", want: &remed, rest: []*capture{&source, &contextc}},
	}
	for _, tt := range tests {
		t.Run(tt.event, func(t *testing.T) {
			before := map[*capture]int64{tt.want: tt.want.hits.Load()}
			for _, c := range tt.rest {
				before[c] = c.hits.Load()
			}

			e := webhook.Event{Type: tt.event, DeliveryID: "d-" + tt.event, Payload: []byte(`{"a":1}`)}
			if err := f.Handle(context.Background(), e); err != nil {
				t.Fatalf("Handle: %v", err)
			}

			if got := tt.want.hits.Load() - before[tt.want]; got != 1 {
				t.Errorf("routed target: %d requests, want 1", got)
			}
			for _, c := range tt.rest {
				if got := c.hits.Load() - before[c]; got != 0 {
					t.Errorf("unrelated target received %d requests, want 0", got)
				}
			}
			if got := tt.want.event.Load(); got != tt.event {
				t.Errorf("X-GitHub-Event = %v, want %s", got, tt.event)
			}
			if got := tt.want.delivery.Load(); got != e.DeliveryID {
				t.Errorf("X-GitHub-Delivery = %v, want %s", got, e.DeliveryID)
			}
			if got := tt.want.body.Load(); got != string(e.Payload) {
				t.Errorf("body = %v, want %s", got, e.Payload)
			}
			// The re-signed signature must be byte-identical to GitHub's, so
			// downstream validation is unchanged.
			if got := tt.want.signature.Load(); got != sign(e.Payload) {
				t.Errorf("X-Hub-Signature-256 = %v, want %s", got, sign(e.Payload))
			}
		})
	}
}

func TestHandleFansOutToMultipleTargets(t *testing.T) {
	var a, b capture
	upA := httptest.NewServer(a.handler(http.StatusAccepted))
	defer upA.Close()
	upB := httptest.NewServer(b.handler(http.StatusAccepted))
	defer upB.Close()

	f := New(Config{Secret: secret, Routes: map[string][]string{
		"issues": {upA.URL, upB.URL},
	}}, slog.New(slog.DiscardHandler))
	if err := f.Handle(context.Background(), webhook.Event{Type: "issues", Payload: []byte(`{}`)}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	for name, c := range map[string]*capture{"a": &a, "b": &b} {
		if got := c.hits.Load(); got != 1 {
			t.Errorf("target %s: %d requests, want 1", name, got)
		}
	}
}

func TestHandleDefaultRoute(t *testing.T) {
	var routed, fallback capture
	upRouted := httptest.NewServer(routed.handler(http.StatusAccepted))
	defer upRouted.Close()
	upFallback := httptest.NewServer(fallback.handler(http.StatusAccepted))
	defer upFallback.Close()

	f := New(Config{Secret: secret, Routes: map[string][]string{
		"issues":     {upRouted.URL},
		DefaultRoute: {upFallback.URL},
	}}, slog.New(slog.DiscardHandler))

	// A plugin-registered event type no rule claims lands on the default.
	if err := f.Handle(context.Background(), webhook.Event{Type: "registry_package", Payload: []byte(`{}`)}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := fallback.hits.Load(); got != 1 {
		t.Errorf("default target: %d requests, want 1", got)
	}
	if got := routed.hits.Load(); got != 0 {
		t.Errorf("routed target received %d requests, want 0", got)
	}

	// An explicitly routed type must NOT also hit the default.
	if err := f.Handle(context.Background(), webhook.Event{Type: "issues", Payload: []byte(`{}`)}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := fallback.hits.Load(); got != 1 {
		t.Errorf("default target: %d requests after routed event, want still 1", got)
	}
	if got := routed.hits.Load(); got != 1 {
		t.Errorf("routed target: %d requests, want 1", got)
	}
}

func TestHandleUnroutedDropsWithoutError(t *testing.T) {
	var c capture
	up := httptest.NewServer(c.handler(http.StatusAccepted))
	defer up.Close()

	f := New(Config{Secret: secret, Routes: map[string][]string{
		"issues": {up.URL},
	}}, slog.New(slog.DiscardHandler))
	if err := f.Handle(context.Background(), webhook.Event{Type: "watch", Payload: []byte(`{}`)}); err != nil {
		t.Fatalf("Handle: unrouted event must not error, got %v", err)
	}
	if got := c.hits.Load(); got != 0 {
		t.Errorf("target received %d requests for an unrouted event, want 0", got)
	}
}

func TestHandlePartialFailure(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		wantErr string
	}{
		{name: "server error", status: http.StatusInternalServerError, wantErr: "status 500"},
		{name: "unauthorized", status: http.StatusUnauthorized, wantErr: "status 401"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ok, bad capture
			upOK := httptest.NewServer(ok.handler(http.StatusAccepted))
			defer upOK.Close()
			upBad := httptest.NewServer(bad.handler(tt.status))
			defer upBad.Close()

			f := New(Config{Secret: secret, Routes: map[string][]string{
				"issues": {upBad.URL, upOK.URL},
			}}, slog.New(slog.DiscardHandler))
			err := f.Handle(context.Background(), webhook.Event{Type: "issues", Payload: []byte(`{}`)})
			if err == nil {
				t.Fatal("Handle: want error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Handle: error %q does not mention %q", err, tt.wantErr)
			}
			// The healthy target must still receive the delivery.
			if got := ok.hits.Load(); got != 1 {
				t.Errorf("healthy target: %d requests, want 1", got)
			}
		})
	}
}

func TestHandleUnreachableTarget(t *testing.T) {
	var ok capture
	upOK := httptest.NewServer(ok.handler(http.StatusAccepted))
	defer upOK.Close()
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close() // connection refused

	f := New(Config{Secret: secret, Routes: map[string][]string{
		"issues": {dead.URL, upOK.URL},
	}}, slog.New(slog.DiscardHandler))
	if err := f.Handle(context.Background(), webhook.Event{Type: "issues", Payload: []byte(`{}`)}); err == nil {
		t.Fatal("Handle: want error, got nil")
	}
	if got := ok.hits.Load(); got != 1 {
		t.Errorf("healthy target: %d requests, want 1", got)
	}
}

func TestHandleTimeout(t *testing.T) {
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.WriteHeader(http.StatusAccepted)
	}))
	defer slow.Close()
	// LIFO: the handler must unblock BEFORE slow.Close() waits for it.
	defer close(release)

	f := New(Config{Secret: secret, Routes: map[string][]string{
		"issues": {slow.URL},
	}, Timeout: 50 * time.Millisecond}, slog.New(slog.DiscardHandler))
	start := time.Now()
	err := f.Handle(context.Background(), webhook.Event{Type: "issues", Payload: []byte(`{}`)})
	if err == nil {
		t.Fatal("Handle: want timeout error, got nil")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Handle: took %v, timeout did not bound the forward", elapsed)
	}
}
