// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"
)

var testLog = slog.New(slog.NewTextHandler(io.Discard, nil))

func sign(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// startServer runs a Server on an ephemeral port and returns its base URL
// and a stop function that asserts a clean drain.
func startServer(t *testing.T, cfg Config, h Handler) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := NewServer(cfg, testLog, h)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.serve(ctx, ln) }()

	return "http://" + ln.Addr().String(), func() {
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("serve() = %v, want context.Canceled", err)
			}
		case <-time.After(15 * time.Second):
			t.Error("server did not drain after cancellation")
		}
	}
}

// post delivers a webhook request and returns the response status; bodies
// are drained and closed here so callers only assert on the code.
func post(t *testing.T, url string, headers map[string]string, body []byte) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func TestDelivery(t *testing.T) {
	secret := []byte("s3cret")
	body := []byte(`{"action":"created"}`)

	var mu sync.Mutex
	var got []Event
	received := make(chan struct{}, 8)
	h := HandlerFunc(func(_ context.Context, e Event) error {
		mu.Lock()
		got = append(got, e)
		mu.Unlock()
		received <- struct{}{}
		return nil
	})

	url, stop := startServer(t, Config{Secret: secret}, h)
	defer stop()

	tests := []struct {
		name    string
		headers map[string]string
		body    []byte
		status  int
		handled bool
	}{
		{
			name: "valid delivery",
			headers: map[string]string{
				"X-Hub-Signature-256": sign(secret, body),
				"X-GitHub-Event":      "code_scanning_alert",
				"X-GitHub-Delivery":   "d-1",
			},
			body:    body,
			status:  http.StatusAccepted,
			handled: true,
		},
		{
			name: "duplicate delivery id",
			headers: map[string]string{
				"X-Hub-Signature-256": sign(secret, body),
				"X-GitHub-Event":      "code_scanning_alert",
				"X-GitHub-Delivery":   "d-1",
			},
			body:   body,
			status: http.StatusAccepted,
		},
		{
			name: "bad signature",
			headers: map[string]string{
				"X-Hub-Signature-256": sign([]byte("wrong"), body),
				"X-GitHub-Event":      "issues",
				"X-GitHub-Delivery":   "d-2",
			},
			body:   body,
			status: http.StatusUnauthorized,
		},
		{
			name: "missing signature",
			headers: map[string]string{
				"X-GitHub-Event":    "issues",
				"X-GitHub-Delivery": "d-3",
			},
			body:   body,
			status: http.StatusUnauthorized,
		},
		{
			name: "ping",
			headers: map[string]string{
				"X-Hub-Signature-256": sign(secret, body),
				"X-GitHub-Event":      "ping",
				"X-GitHub-Delivery":   "d-4",
			},
			body:   body,
			status: http.StatusNoContent,
		},
		{
			name: "missing event header",
			headers: map[string]string{
				"X-Hub-Signature-256": sign(secret, body),
				"X-GitHub-Delivery":   "d-5",
			},
			body:   body,
			status: http.StatusBadRequest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := post(t, url+"/webhook", tt.headers, tt.body); got != tt.status {
				t.Errorf("status = %d, want %d", got, tt.status)
			}
			if tt.handled {
				select {
				case <-received:
				case <-time.After(5 * time.Second):
					t.Fatal("handler never received the delivery")
				}
			}
		})
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("handled %d events, want exactly 1 (dedup)", len(got))
	}
	if got[0].Type != "code_scanning_alert" || got[0].DeliveryID != "d-1" || !bytes.Equal(got[0].Payload, body) {
		t.Errorf("handled event = %+v, want type/delivery/payload preserved", got[0])
	}
}

func TestHealthEndpoints(t *testing.T) {
	ready := false
	url, stop := startServer(t, Config{
		Secret: []byte("s"),
		Ready:  func() bool { return ready },
	}, HandlerFunc(func(context.Context, Event) error { return nil }))
	defer stop()

	get := func(path string) int {
		resp, err := http.Get(url + path)
		if err != nil {
			t.Fatalf("get %s: %v", path, err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	if got := get("/healthz"); got != http.StatusOK {
		t.Errorf("/healthz = %d, want 200", got)
	}
	if got := get("/readyz"); got != http.StatusServiceUnavailable {
		t.Errorf("/readyz before ready = %d, want 503", got)
	}
	ready = true
	if got := get("/readyz"); got != http.StatusOK {
		t.Errorf("/readyz after ready = %d, want 200", got)
	}
}

func TestQueueFull(t *testing.T) {
	secret := []byte("s")
	block := make(chan struct{})
	h := HandlerFunc(func(_ context.Context, _ Event) error {
		<-block
		return nil
	})
	url, stop := startServer(t, Config{Secret: secret, Workers: 1, QueueSize: 1}, h)
	defer func() {
		close(block)
		stop()
	}()

	// First delivery occupies the single worker, second fills the queue;
	// the worker may not have picked up the first yet, so allow one extra
	// before demanding a 503.
	saw503 := false
	for i := range 4 {
		body := fmt.Appendf(nil, `{"n":%d}`, i)
		status := post(t, url+"/webhook", map[string]string{
			"X-Hub-Signature-256": sign(secret, body),
			"X-GitHub-Event":      "issues",
			"X-GitHub-Delivery":   "q-" + strconv.Itoa(i),
		}, body)
		if status == http.StatusServiceUnavailable {
			saw503 = true
			break
		}
		if status != http.StatusAccepted {
			t.Fatalf("delivery %d: status %d, want 202 or 503", i, status)
		}
	}
	if !saw503 {
		t.Error("queue never reported full; expected a 503 once workers and queue were saturated")
	}
}

func TestDedupEviction(t *testing.T) {
	d := newDedup(2, time.Minute)
	if !d.add("a") || !d.add("b") {
		t.Fatal("fresh ids reported as duplicates")
	}
	if d.add("a") {
		t.Error("a not deduped while in window")
	}
	if !d.add("c") { // evicts a
		t.Fatal("c reported as duplicate")
	}
	if !d.add("a") {
		t.Error("a still deduped after eviction")
	}
	d.remove("c")
	if !d.add("c") {
		t.Error("c still deduped after remove")
	}
}

func TestDedupReset(t *testing.T) {
	d := newDedup(2, time.Minute)
	if !d.add("a") || !d.add("b") {
		t.Fatal("fresh ids reported as duplicates")
	}
	d.reset()
	if !d.add("a") || !d.add("b") {
		t.Error("ids still deduped after reset")
	}
	if d.add("a") {
		t.Error("a not deduped after re-add; reset broke the window")
	}
}

func TestDedupTTL(t *testing.T) {
	now := time.Unix(0, 0)
	d := newDedup(4, 5*time.Minute)
	d.now = func() time.Time { return now }

	if !d.add("a") {
		t.Fatal("fresh id reported as duplicate")
	}
	now = now.Add(4 * time.Minute)
	if d.add("a") {
		t.Error("a not deduped inside the TTL")
	}
	now = now.Add(time.Minute) // 5m after the original add
	if !d.add("a") {
		t.Error("a still deduped once the TTL elapsed")
	}
	now = now.Add(time.Minute)
	if d.add("a") {
		t.Error("refreshed a not deduped inside its new TTL")
	}
}

func TestValidSignature(t *testing.T) {
	secret := []byte("top")
	body := []byte("payload")
	tests := []struct {
		name   string
		secret []byte
		header string
		want   bool
	}{
		{"valid", secret, sign(secret, body), true},
		{"wrong secret", []byte("other"), sign(secret, body), false},
		{"empty header", secret, "", false},
		{"no prefix", secret, "deadbeef", false},
		{"bad hex", secret, "sha256=zz", false},
		{"empty secret", nil, sign(nil, body), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validSignature(tt.secret, body, tt.header); got != tt.want {
				t.Errorf("validSignature() = %v, want %v", got, tt.want)
			}
		})
	}
}
