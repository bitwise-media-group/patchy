// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package ghclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeProxy is an httptest server standing in for an HTTP forward proxy.
// The clients under test target a plain-http GitHub base URL, so proxied
// requests arrive in absolute form (GET http://host/path) rather than as
// CONNECT tunnels — the proxy answers as the origin while recording what
// traversed it.
type fakeProxy struct {
	mu   sync.Mutex
	urls []string
	mux  *http.ServeMux
}

// newFakeProxy starts a recording forward proxy; register origin handlers on
// its mux with bare REST paths under /api/v3 (the enterprise-URL prefix).
func newFakeProxy(t *testing.T) (*fakeProxy, string) {
	t.Helper()
	p := &fakeProxy{mux: http.NewServeMux()}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.urls = append(p.urls, r.URL.String())
		p.mu.Unlock()
		p.mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return p, srv.URL
}

// seen returns the absolute request URLs the proxy handled.
func (p *fakeProxy) seen() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.urls...)
}

func TestNewTokenWithProxy(t *testing.T) {
	proxy, proxyURL := newFakeProxy(t)
	proxy.mux.HandleFunc("GET /api/v3/repos/o/r", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, `{"default_branch":"main"}`)
	})

	c, err := NewToken("pat", "http://gh.test", WithProxy(proxyURL))
	if err != nil {
		t.Fatalf("NewToken() error = %v", err)
	}
	branch, err := c.DefaultBranch(context.Background(), Repo{Owner: "o", Name: "r"})
	if err != nil {
		t.Fatalf("DefaultBranch() error = %v", err)
	}
	if branch != "main" {
		t.Errorf("DefaultBranch() = %q, want %q", branch, "main")
	}
	urls := proxy.seen()
	if len(urls) != 1 {
		t.Fatalf("proxy handled %d requests, want 1: %v", len(urls), urls)
	}
	if !strings.HasPrefix(urls[0], "http://gh.test/") {
		t.Errorf("proxied request URL = %q, want absolute form targeting gh.test", urls[0])
	}
}

func TestNewAppProxyURL(t *testing.T) {
	proxy, proxyURL := newFakeProxy(t)
	proxy.mux.HandleFunc("GET /api/v3/repos/o/r/installation", func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); !strings.HasPrefix(auth, "Bearer ") {
			t.Errorf("installation lookup Authorization = %q, want app JWT bearer", auth)
		}
		writeJSON(t, w, `{"id": 42}`)
	})
	proxy.mux.HandleFunc("POST /api/v3/app/installations/42/access_tokens", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, `{"token":"inst-tok","expires_at":"2100-01-01T00:00:00Z"}`)
	})

	app, err := NewApp(AppConfig{
		AppID: 7, PrivateKey: testPrivateKeyPEM(t), BaseURL: "http://gh.test", ProxyURL: proxyURL,
	})
	if err != nil {
		t.Fatalf("NewApp() error = %v", err)
	}
	tok, _, err := app.ScopedToken(context.Background(), Repo{Owner: "o", Name: "r"}, TokenPerms{Contents: "read"})
	if err != nil {
		t.Fatalf("ScopedToken() error = %v", err)
	}
	if tok != "inst-tok" {
		t.Errorf("ScopedToken() = %q, want %q", tok, "inst-tok")
	}
	// Both the JWT-authenticated installation lookup and the token mint must
	// traverse the proxy.
	if urls := proxy.seen(); len(urls) != 2 {
		t.Errorf("proxy handled %d requests, want 2: %v", len(urls), urls)
	}
}

// TestProxyPrecedence: a spec proxy wins over the process proxy environment —
// the env proxy sees nothing. (No env-only routing assertion exists on
// purpose: ProxyFromEnvironment caches the environment in a process-wide
// sync.Once, so t.Setenv cannot reliably exercise that path.)
func TestProxyPrecedence(t *testing.T) {
	envProxy, envURL := newFakeProxy(t)
	t.Setenv("HTTP_PROXY", envURL)
	t.Setenv("HTTPS_PROXY", envURL)

	specProxy, specURL := newFakeProxy(t)
	specProxy.mux.HandleFunc("GET /api/v3/repos/o/r", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, `{"default_branch":"main"}`)
	})

	c, err := NewToken("pat", "http://gh.test", WithProxy(specURL))
	if err != nil {
		t.Fatalf("NewToken() error = %v", err)
	}
	if _, err := c.DefaultBranch(context.Background(), Repo{Owner: "o", Name: "r"}); err != nil {
		t.Fatalf("DefaultBranch() error = %v", err)
	}
	if got := len(specProxy.seen()); got != 1 {
		t.Errorf("spec proxy handled %d requests, want 1", got)
	}
	if got := len(envProxy.seen()); got != 0 {
		t.Errorf("env proxy handled %d requests, want 0 (spec proxy must win)", got)
	}
}

func TestParseProxyRejections(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{"socks scheme", "socks5://proxy.example:1080"},
		{"schemeless", "proxy.example:3128"},
		{"missing host", "http://"},
		{"unparseable", "http://bad url with spaces"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewToken("pat", "", WithProxy(tt.raw)); err == nil {
				t.Errorf("NewToken(WithProxy(%q)) error = nil, want non-nil", tt.raw)
			}
			if err := ValidateProxy(tt.raw); err == nil {
				t.Errorf("ValidateProxy(%q) = nil, want non-nil", tt.raw)
			}
		})
	}
}

func TestWithProxyEmptyIsNoop(t *testing.T) {
	if err := ValidateProxy(""); err != nil {
		t.Errorf("ValidateProxy(\"\") = %v, want nil", err)
	}
	mux, base := newFake(t)
	mux.HandleFunc("GET /repos/o/r", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, `{"default_branch":"main"}`)
	})
	c, err := NewToken("pat", base, WithProxy(""))
	if err != nil {
		t.Fatalf("NewToken(WithProxy(\"\")) error = %v", err)
	}
	branch, err := c.DefaultBranch(context.Background(), Repo{Owner: "o", Name: "r"})
	if err != nil {
		t.Fatalf("DefaultBranch() error = %v", err)
	}
	if branch != "main" {
		t.Errorf("DefaultBranch() = %q, want %q", branch, "main")
	}
}
