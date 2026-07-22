// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bitwise-media-group/patchy/internal/web/auth"
	"github.com/bitwise-media-group/patchy/internal/web/authz"
)

// stubAuth resolves every request to a fixed identity; nil means no session.
type stubAuth struct {
	id *auth.Identity
}

func (s stubAuth) Identify(http.ResponseWriter, *http.Request) (*auth.Identity, error) {
	return s.id, nil
}

func (stubAuth) Register(*http.ServeMux) {}

// stubGranter returns fixed grants.
type stubGranter struct {
	grants authz.Grants
	err    error
}

func (s stubGranter) Grants(context.Context, auth.Identity) (authz.Grants, error) {
	return s.grants, s.err
}

// operator is a signed-in identity with every grant, for handler tests.
var operator = &auth.Identity{Username: "op@acme.test", DisplayName: "Op", Session: true}

func allGrants() authz.Grants {
	return authz.Grants{
		View:  true,
		Verbs: append([]string(nil), authz.ActionVerbs...),
		Admin: append([]string(nil), authz.AdminVerbs...),
	}
}

func TestHandlerSecurityHeaders(t *testing.T) {
	s := testServer(t)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	res, err := http.Get(ts.URL + "/api/rollups")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	for header, want := range map[string]string{
		"X-Frame-Options":        "DENY",
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "same-origin",
		"Cache-Control":          "no-store",
	} {
		if got := res.Header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}

	page, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer func() { _ = page.Body.Close() }()
	if got := page.Header.Get("Cache-Control"); got == "no-store" {
		t.Error("static surface should not be no-store")
	}
}

func TestFindingsRequiresSessionAndViewGrant(t *testing.T) {
	cases := []struct {
		name       string
		auth       stubAuth
		granter    stubGranter
		wantStatus int
		wantBody   string
	}{
		{
			name:       "no session",
			auth:       stubAuth{},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "no view grant",
			auth:       stubAuth{id: operator},
			granter:    stubGranter{grants: authz.Grants{Verbs: []string{"approve"}}},
			wantStatus: http.StatusForbidden,
			wantBody:   `Permission denied. User "Op" may not view findings in namespace "patchy".`,
		},
		{
			name:       "granted",
			auth:       stubAuth{id: operator},
			granter:    stubGranter{grants: allGrants()},
			wantStatus: http.StatusOK,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := testServer(t, fullFinding())
			s.auth, s.granter = tc.auth, tc.granter
			ts := httptest.NewServer(s.Handler())
			defer ts.Close()

			res, err := http.Get(ts.URL + "/api/findings")
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			defer func() { _ = res.Body.Close() }()
			body, _ := io.ReadAll(res.Body)
			if res.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d", res.StatusCode, tc.wantStatus)
			}
			if tc.wantBody != "" && strings.TrimSpace(string(body)) != tc.wantBody {
				t.Errorf("body = %q, want %q", strings.TrimSpace(string(body)), tc.wantBody)
			}
			if tc.wantStatus == http.StatusOK {
				var ds Dataset
				if err := json.Unmarshal(body, &ds); err != nil {
					t.Fatalf("decode: %v", err)
				}
				if len(ds.Findings) != 1 || ds.User == nil || !ds.User.LoggedIn {
					t.Errorf("dataset findings=%d user=%+v", len(ds.Findings), ds.User)
				}
				if got := ds.Findings[0].UserActions; len(got) != len(authz.ActionVerbs) {
					t.Errorf("userActions = %v", got)
				}
			}
		})
	}
}

func TestRollupsIsPublic(t *testing.T) {
	s := testServer(t, fullFinding(), testRollup("total", "", "total"))
	// No session at all — the unconfigured posture.
	s.auth = stubAuth{}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	res, err := http.Get(ts.URL + "/api/rollups")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	var m map[string]any
	if err := json.NewDecoder(res.Body).Decode(&m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := m["findings"].([]any); len(got) != 0 {
		t.Errorf("public rollups carried %d findings", len(got))
	}
	if _, ok := m["user"]; ok {
		t.Error("public rollups carried user")
	}
	if _, ok := m["rollups"]; !ok {
		t.Error("public rollups missing rollups")
	}
}

func TestCrossSitePostRejected(t *testing.T) {
	s := testServer(t, fullFinding())
	s.auth, s.granter = stubAuth{id: operator}, stubGranter{grants: allGrants()}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost,
		ts.URL+"/api/findings/gh-cs-orders-1/actions/suspend", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", res.StatusCode)
	}
}

func TestStaticHandlerStub(t *testing.T) {
	if _, ok := uiAssets(); ok {
		t.Skip("compiled with -tags withui; stub not in use")
	}
	s := testServer(t)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	res, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", res.StatusCode)
	}
	if !strings.Contains(string(body), "not bundled") {
		t.Errorf("stub body = %q", body)
	}
}
