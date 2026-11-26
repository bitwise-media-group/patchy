// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package ghuser

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestWebURLAndHost(t *testing.T) {
	cases := []struct{ base, web, host string }{
		{"", "https://github.com", "github.com"},
		{"https://api.github.com/", "https://github.com", "github.com"},
		{"https://ghes.acme.test", "https://ghes.acme.test", "ghes.acme.test"},
		{"https://GHES.acme.test/api/v3/", "https://GHES.acme.test", "ghes.acme.test"},
		{"ghes.acme.test:8443", "https://ghes.acme.test:8443", "ghes.acme.test"},
	}
	for _, tc := range cases {
		if got := WebURL(tc.base); got != tc.web {
			t.Errorf("WebURL(%q) = %q, want %q", tc.base, got, tc.web)
		}
		if got := Host(tc.base); got != tc.host {
			t.Errorf("Host(%q) = %q, want %q", tc.base, got, tc.host)
		}
	}
}

func TestAuthorizeURL(t *testing.T) {
	raw := AuthorizeURL("https://ghes.acme.test/api/v3", "Iv1.abc", "https://status.test/oauth2/github/gh/callback",
		"sealed-state", "challenge")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme+"://"+u.Host+u.Path != "https://ghes.acme.test/login/oauth/authorize" {
		t.Errorf("authorize endpoint = %s", raw)
	}
	q := u.Query()
	for k, want := range map[string]string{
		"client_id": "Iv1.abc", "redirect_uri": "https://status.test/oauth2/github/gh/callback",
		"state": "sealed-state", "code_challenge": "challenge", "code_challenge_method": "S256",
	} {
		if got := q.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if q.Has("scope") {
		t.Error("a GitHub App user flow takes no scopes")
	}
}

func TestMaintains(t *testing.T) {
	for role, want := range map[string]bool{
		RoleAdmin: true, RoleMaintain: true, RoleWrite: false, RoleTriage: false, RoleRead: false, RoleNone: false,
	} {
		if Maintains(role) != want {
			t.Errorf("Maintains(%q) = %v", role, !want)
		}
	}
}

func TestClient(t *testing.T) {
	var gotToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get(TokenHeader)
		switch r.URL.Path {
		case PathRepositories:
			var req RepositoriesRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.AccessToken == "expired" {
				WriteError(w, &Error{Code: CodeUserToken, Message: "GitHub rejected the user token"})
				return
			}
			_ = json.NewEncoder(w).Encode(RepositoriesResponse{Repositories: []Repository{{Name: "a/b", Role: RoleAdmin}}})
		default:
			http.Error(w, "boom", http.StatusInternalServerError)
		}
	}))
	defer srv.Close()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("sa-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &Client{URL: srv.URL, TokenFile: tokenFile}

	res, err := c.Repositories(t.Context(), RepositoriesRequest{Integration: "gh", AccessToken: "ghu"})
	if err != nil || len(res.Repositories) != 1 || gotToken != "sa-token" {
		t.Fatalf("Repositories = %+v, %v (caller token %q)", res, err, gotToken)
	}
	_, err = c.Repositories(t.Context(), RepositoriesRequest{Integration: "gh", AccessToken: "expired"})
	if !IsCode(err, CodeUserToken) {
		t.Errorf("expired token err = %v, want CodeUserToken", err)
	}
	// A non-contract failure still decodes to an Error.
	_, err = c.Refresh(t.Context(), RefreshRequest{Integration: "gh", RefreshToken: "r"})
	if !IsCode(err, CodeUpstream) {
		t.Errorf("server failure err = %v, want CodeUpstream", err)
	}
	// Validation runs before any call.
	if _, err := c.Repositories(t.Context(), RepositoriesRequest{Integration: "gh"}); !IsCode(err, CodeBadRequest) {
		t.Errorf("empty token err = %v, want CodeBadRequest", err)
	}
	if _, err := (&Client{}).Exchange(t.Context(), ExchangeRequest{}); !errors.Is(err, ErrUnavailable) {
		t.Errorf("unconfigured err = %v, want ErrUnavailable", err)
	}
}
