// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package wizapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeWiz stands in for both the token endpoint and the GraphQL API.
type fakeWiz struct {
	tokens   atomic.Int64
	mutation struct {
		query     string
		variables map[string]any
	}
	graphqlStatus int
	graphqlBody   string
}

func (f *fakeWiz) start(t *testing.T) *Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if r.PostForm.Get("grant_type") != "client_credentials" ||
			r.PostForm.Get("audience") != "wiz-api" ||
			r.PostForm.Get("client_id") != "id" ||
			r.PostForm.Get("client_secret") != "sec" {
			http.Error(w, "bad grant", http.StatusUnauthorized)
			return
		}
		f.tokens.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "tok-abc", "token_type": "Bearer", "expires_in": 3600,
		})
	})
	mux.HandleFunc("POST /graphql", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-abc" {
			http.Error(w, "no token", http.StatusUnauthorized)
			return
		}
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mutation.query, f.mutation.variables = req.Query, req.Variables
		status := f.graphqlStatus
		if status == 0 {
			status = http.StatusOK
		}
		body := f.graphqlBody
		if body == "" {
			body = `{"data": {"updateIssue": {"issue": {"id": "iss-1", "status": "REJECTED"}}}}`
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c, err := New(Options{
		Endpoint:     srv.URL + "/graphql",
		TokenURL:     srv.URL + "/oauth/token",
		ClientID:     "id",
		ClientSecret: "sec",
		HTTPClient:   srv.Client(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestRejectIssue(t *testing.T) {
	f := &fakeWiz{}
	c := f.start(t)

	if err := c.RejectIssue(t.Context(), "iss-1", ResolutionFalsePositive, "not exploitable"); err != nil {
		t.Fatalf("RejectIssue() = %v, want nil", err)
	}
	if !strings.Contains(f.mutation.query, "updateIssue") {
		t.Errorf("mutation query = %q, want updateIssue", f.mutation.query)
	}
	patch, _ := f.mutation.variables["patch"].(map[string]any)
	if f.mutation.variables["id"] != "iss-1" ||
		patch["status"] != "REJECTED" ||
		patch["resolutionReason"] != "FALSE_POSITIVE" ||
		patch["note"] != "not exploitable" {
		t.Errorf("mutation variables = %v, want id/status/reason/note", f.mutation.variables)
	}
}

// The token is fetched once and reused across calls until expiry.
func TestTokenIsCached(t *testing.T) {
	f := &fakeWiz{}
	c := f.start(t)
	for range 3 {
		if err := c.RejectIssue(t.Context(), "iss-1", ResolutionWontFix, ""); err != nil {
			t.Fatalf("RejectIssue() = %v, want nil", err)
		}
	}
	if got := f.tokens.Load(); got != 1 {
		t.Errorf("token endpoint hit %d times, want 1", got)
	}
}

func TestGraphQLErrorsSurface(t *testing.T) {
	f := &fakeWiz{graphqlBody: `{"errors": [{"message": "issue not found"}]}`}
	c := f.start(t)
	err := c.RejectIssue(t.Context(), "iss-404", ResolutionWontFix, "")
	if err == nil || !strings.Contains(err.Error(), "issue not found") {
		t.Errorf("RejectIssue() = %v, want the GraphQL error surfaced", err)
	}
}

func TestHTTPErrorsSurface(t *testing.T) {
	f := &fakeWiz{graphqlStatus: http.StatusBadGateway, graphqlBody: "upstream sad"}
	c := f.start(t)
	err := c.RejectIssue(t.Context(), "iss-1", ResolutionWontFix, "")
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Errorf("RejectIssue() = %v, want the HTTP status surfaced", err)
	}
}

func TestNewValidation(t *testing.T) {
	if _, err := New(Options{ClientID: "id", ClientSecret: "s"}); err == nil {
		t.Error("New(no endpoint) = nil error, want one")
	}
	if _, err := New(Options{Endpoint: "https://x/graphql"}); err == nil {
		t.Error("New(no credentials) = nil error, want one")
	}
}
