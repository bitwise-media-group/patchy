// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package broker

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

const (
	testNamespace = "patchy-agents"
	testSA        = "patchy-agent"
	testSubject   = "system:serviceaccount:" + testNamespace + ":" + testSA
)

// fakeReviews wires a fake clientset whose TokenReview verdict is table-driven
// by token value: "good" authenticates as the agent SA (pod agent-pod-1),
// "other" as a foreign identity, everything else fails authentication.
func fakeReviews(calls *atomic.Int64) *fake.Clientset {
	cs := fake.NewClientset()
	cs.PrependReactor("create", "tokenreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if calls != nil {
			calls.Add(1)
		}
		tr := action.(k8stesting.CreateAction).GetObject().(*authnv1.TokenReview)
		out := tr.DeepCopy()
		switch tr.Spec.Token {
		case "good":
			out.Status = authnv1.TokenReviewStatus{
				Authenticated: true,
				User: authnv1.UserInfo{
					Username: testSubject,
					Extra: map[string]authnv1.ExtraValue{
						podNameExtraKey: {"agent-pod-1"},
					},
				},
			}
		case "other":
			out.Status = authnv1.TokenReviewStatus{
				Authenticated: true,
				User:          authnv1.UserInfo{Username: "system:serviceaccount:other:sa"},
			}
		default:
			out.Status = authnv1.TokenReviewStatus{Authenticated: false}
		}
		return true, out, nil
	})
	return cs
}

func mustTarget(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := ParseTarget(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func newTestServer(t *testing.T, cfg Config, calls *atomic.Int64) *Server {
	t.Helper()
	cfg.AgentNamespace = testNamespace
	cfg.AgentServiceAccount = testSA
	s, err := New(fakeReviews(calls), cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func keyFile(t *testing.T, key string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "api-key")
	if err := os.WriteFile(path, []byte(key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAuthRequired(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	s := newTestServer(t, Config{
		Upstreams: map[string]Upstream{"anthropic": {Target: mustTarget(t, upstream.URL)}},
	}, nil)
	h := s.Handler()

	tests := []struct {
		name  string
		token string
		want  int
	}{
		{"no token", "", http.StatusUnauthorized},
		{"bad token", "expired", http.StatusUnauthorized},
		{"foreign identity", "other", http.StatusUnauthorized},
		{"agent identity", "good", http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", nil)
			if tt.token != "" {
				req.Header.Set(TokenHeader, tt.token)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
			if tt.want == http.StatusUnauthorized {
				var e struct {
					Type string `json:"type"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || e.Type != "error" {
					t.Fatalf("401 body is not the error envelope: %s", rec.Body.String())
				}
			}
		})
	}
}

func TestVerdictCache(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	var calls atomic.Int64
	s := newTestServer(t, Config{
		Upstreams: map[string]Upstream{"anthropic": {Target: mustTarget(t, upstream.URL)}},
	}, &calls)
	h := s.Handler()

	for range 3 {
		req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", nil)
		req.Header.Set(TokenHeader, "good")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("token reviews = %d, want 1 (verdict cached)", got)
	}

	// Denials are cached too: retrying a bad token must not add QPS.
	for range 3 {
		req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", nil)
		req.Header.Set(TokenHeader, "expired")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d", rec.Code)
		}
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("token reviews = %d, want 2 (denial cached)", got)
	}
}

func TestAnthropicInjectionAndStripping(t *testing.T) {
	var got *http.Request
	var gotHeader http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, gotHeader = r.Clone(r.Context()), r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	s := newTestServer(t, Config{
		Upstreams: map[string]Upstream{
			"anthropic": Anthropic(mustTarget(t, upstream.URL), keyFile(t, "sk-test-123"), false),
		},
	}, nil)

	req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", strings.NewReader(`{}`))
	req.Header.Set(TokenHeader, "good")
	req.Header.Set("Authorization", "Bearer caller-supplied")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("anthropic-beta", "prompt-caching-2024-07-31")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if got.URL.Path != "/v1/messages" {
		t.Errorf("upstream path = %q, want /v1/messages (route prefix stripped)", got.URL.Path)
	}
	if k := gotHeader.Get("x-api-key"); k != "sk-test-123" {
		t.Errorf("x-api-key = %q, want the mounted key", k)
	}
	if v := gotHeader.Get(TokenHeader); v != "" {
		t.Errorf("caller token forwarded upstream: %q", v)
	}
	if v := gotHeader.Get("Authorization"); v != "" {
		t.Errorf("caller Authorization forwarded upstream: %q", v)
	}
	if v := gotHeader.Get("anthropic-version"); v != "2023-06-01" {
		t.Errorf("anthropic-version = %q, want passthrough", v)
	}
	if v := gotHeader.Get("anthropic-beta"); v != "prompt-caching-2024-07-31" {
		t.Errorf("anthropic-beta = %q, want passthrough", v)
	}
}

func TestAnthropicBearerMode(t *testing.T) {
	var gotHeader http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	// bearer=true: a `claude setup-token` OAuth token rides Authorization,
	// not x-api-key — and the caller's own Authorization must not survive.
	s := newTestServer(t, Config{
		Upstreams: map[string]Upstream{
			"anthropic": Anthropic(mustTarget(t, upstream.URL), keyFile(t, "sk-ant-oat01-xyz"), true),
		},
	}, nil)

	req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", strings.NewReader(`{}`))
	req.Header.Set(TokenHeader, "good")
	req.Header.Set("Authorization", "Bearer caller-supplied")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if v := gotHeader.Get("Authorization"); v != "Bearer sk-ant-oat01-xyz" {
		t.Errorf("Authorization = %q, want the mounted OAuth token as a bearer", v)
	}
	if v := gotHeader.Get("x-api-key"); v != "" {
		t.Errorf("x-api-key = %q, want none in bearer mode", v)
	}
}

func TestBedrockSigning(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIATEST")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")

	var gotHeader http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	up, err := Bedrock(t.Context(), mustTarget(t, upstream.URL), "us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t, Config{Upstreams: map[string]Upstream{"bedrock": up}}, nil)

	body := `{"anthropic_version":"bedrock-2023-05-31"}`
	req := httptest.NewRequest(http.MethodPost,
		"/bedrock/model/us.anthropic.claude-sonnet-5/invoke-with-response-stream", strings.NewReader(body))
	req.Header.Set(TokenHeader, "good")
	req.Header.Set("X-Amz-Security-Token", "caller-junk")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	authz := gotHeader.Get("Authorization")
	if !strings.HasPrefix(authz, "AWS4-HMAC-SHA256 ") {
		t.Errorf("Authorization = %q, want a SigV4 signature", authz)
	}
	if !strings.Contains(authz, "/us-east-1/bedrock/aws4_request") {
		t.Errorf("Authorization = %q, want region/service scope", authz)
	}
	if gotHeader.Get("X-Amz-Date") == "" {
		t.Error("X-Amz-Date missing from signed request")
	}
	if v := gotHeader.Get("X-Amz-Security-Token"); v == "caller-junk" {
		t.Error("caller X-Amz-Security-Token survived to the signed request")
	}
}

func TestBedrockBodyTooLarge(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIATEST")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")

	upstream := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("oversized request reached the upstream")
	}))
	defer upstream.Close()

	up, err := Bedrock(t.Context(), mustTarget(t, upstream.URL), "us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t, Config{
		MaxRequestBytes: 16,
		Upstreams:       map[string]Upstream{"bedrock": up},
	}, nil)

	req := httptest.NewRequest(http.MethodPost, "/bedrock/model/m/invoke",
		strings.NewReader(strings.Repeat("x", 64)))
	req.Header.Set(TokenHeader, "good")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}

func TestUpstreamErrorEnvelope(t *testing.T) {
	// A target nothing listens on: the proxy must answer with the JSON error
	// envelope, not a bare 502.
	s := newTestServer(t, Config{
		Upstreams: map[string]Upstream{
			"anthropic": {Target: mustTarget(t, "http://127.0.0.1:1")},
		},
	}, nil)

	req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", nil)
	req.Header.Set(TokenHeader, "good")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	var e struct {
		Type  string `json:"type"`
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || e.Type != "error" || e.Error.Type != "api_error" {
		t.Fatalf("502 body is not the error envelope: %s", rec.Body.String())
	}
}

func TestSSEPingInjection(t *testing.T) {
	// The upstream opens an event stream, sends one event, then goes silent
	// longer than the ping interval before closing.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "event: message_start\ndata: {}\n\n")
		w.(http.Flusher).Flush()
		time.Sleep(120 * time.Millisecond)
		_, _ = io.WriteString(w, "event: message_stop\ndata: {}\n\n")
	}))
	defer upstream.Close()

	s := newTestServer(t, Config{
		PingInterval: 30 * time.Millisecond,
		Upstreams:    map[string]Upstream{"anthropic": {Target: mustTarget(t, upstream.URL)}},
	}, nil)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/anthropic/v1/messages", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(TokenHeader, "good")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte(": ping\n\n")) {
		t.Errorf("no keep-alive ping in the streamed body:\n%s", body)
	}
	if !bytes.Contains(body, []byte("message_stop")) {
		t.Errorf("stream truncated before the final event:\n%s", body)
	}
}

func TestSSENoPingOnNonStream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		time.Sleep(80 * time.Millisecond)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()

	s := newTestServer(t, Config{
		PingInterval: 20 * time.Millisecond,
		Upstreams:    map[string]Upstream{"anthropic": {Target: mustTarget(t, upstream.URL)}},
	}, nil)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/anthropic/v1/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(TokenHeader, "good")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if bytes.Contains(body, []byte(": ping")) {
		t.Errorf("ping injected into a non-SSE response:\n%s", body)
	}
}

func TestReadyz(t *testing.T) {
	good := keyFile(t, "sk-ok")
	s := newTestServer(t, Config{
		Upstreams: map[string]Upstream{
			"anthropic": Anthropic(mustTarget(t, "https://api.anthropic.com"), good, false),
		},
	}, nil)
	rec := httptest.NewRecorder()
	s.HealthHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("readyz = %d, want 200", rec.Code)
	}

	missing := filepath.Join(t.TempDir(), "absent")
	s = newTestServer(t, Config{
		Upstreams: map[string]Upstream{
			"anthropic": Anthropic(mustTarget(t, "https://api.anthropic.com"), missing, false),
		},
	}, nil)
	rec = httptest.NewRecorder()
	s.HealthHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz = %d, want 503 when the key file is unreadable", rec.Code)
	}

	rec = httptest.NewRecorder()
	s.HealthHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz = %d, want 200 regardless of credential state", rec.Code)
	}
}

func TestAuditLine(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()

	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	cfg := Config{
		AgentNamespace:      testNamespace,
		AgentServiceAccount: testSA,
		Upstreams:           map[string]Upstream{"anthropic": {Target: mustTarget(t, upstream.URL)}},
	}
	s, err := New(fakeReviews(nil), cfg, log)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", strings.NewReader("{}"))
	req.Header.Set(TokenHeader, "good")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("audit line is not JSON: %v: %s", err, buf.String())
	}
	for _, key := range []string{"pod", "route", "method", "path", "status", "duration", "bytes"} {
		if _, ok := line[key]; !ok {
			t.Errorf("audit line missing %q: %s", key, buf.String())
		}
	}
	if line["pod"] != "agent-pod-1" {
		t.Errorf("audit pod = %v, want the token's bound pod", line["pod"])
	}
	if strings.Contains(buf.String(), "{}") && strings.Contains(buf.String(), "body") {
		t.Error("audit line carries a body")
	}
}

func TestConfigValidation(t *testing.T) {
	if _, err := New(fake.NewClientset(), Config{
		AgentNamespace: testNamespace, AgentServiceAccount: testSA,
	}, nil); err == nil {
		t.Error("no-upstream config accepted")
	}
	if _, err := New(fake.NewClientset(), Config{
		Upstreams: map[string]Upstream{"anthropic": {Target: &url.URL{Scheme: "https", Host: "x"}}},
	}, nil); err == nil {
		t.Error("missing agent identity accepted")
	}
	if _, err := ParseTarget("not a url"); err == nil {
		t.Error("relative target accepted")
	}
}
