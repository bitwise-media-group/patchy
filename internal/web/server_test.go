// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package web

import (
	"compress/gzip"
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

// TestFindingDetailAuthAndNotFound pins the detail route to the same view
// gate as the list — finding data must not leak through the per-name path —
// and maps an absent (TTL-expired) finding onto 404.
func TestFindingDetailAuthAndNotFound(t *testing.T) {
	cases := []struct {
		name       string
		path       string
		auth       stubAuth
		granter    stubGranter
		wantStatus int
	}{
		{
			name:       "no session",
			path:       "/api/findings/gh-cs-orders-1",
			auth:       stubAuth{},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "no view grant",
			path:       "/api/findings/gh-cs-orders-1",
			auth:       stubAuth{id: operator},
			granter:    stubGranter{grants: authz.Grants{Verbs: []string{"approve"}}},
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "granted",
			path:       "/api/findings/gh-cs-orders-1",
			auth:       stubAuth{id: operator},
			granter:    stubGranter{grants: allGrants()},
			wantStatus: http.StatusOK,
		},
		{
			name:       "expired finding",
			path:       "/api/findings/gone-1",
			auth:       stubAuth{id: operator},
			granter:    stubGranter{grants: allGrants()},
			wantStatus: http.StatusNotFound,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := testServer(t, fullFinding())
			s.auth, s.granter = tc.auth, tc.granter
			ts := httptest.NewServer(s.Handler())
			defer ts.Close()

			res, err := http.Get(ts.URL + tc.path)
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			defer func() { _ = res.Body.Close() }()
			if res.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d", res.StatusCode, tc.wantStatus)
			}
			if tc.wantStatus != http.StatusOK {
				return
			}
			var f Finding
			if err := json.NewDecoder(res.Body).Decode(&f); err != nil {
				t.Fatalf("decode: %v", err)
			}
			// The detail carries what the list omits.
			if f.Description == "" || len(f.Alerts) == 0 || len(f.PhaseTimes) == 0 {
				t.Errorf("detail = %+v, want description/alerts/phaseTimes", f)
			}
			if got := f.UserActions; len(got) != len(authz.ActionVerbs) {
				t.Errorf("userActions = %v", got)
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

// The dataset endpoints negotiate gzip: encoded when the client accepts it,
// identity otherwise, and Vary set either way so caches keep them apart.
func TestDatasetGzipNegotiation(t *testing.T) {
	s := testServer(t, fullFinding(), testRollup("total", "", "total"))
	s.auth = stubAuth{}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	// Explicit Accept-Encoding, transparent decompression off, so the wire
	// encoding is observable.
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/rollups", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept-Encoding", "gzip")
	res, err := (&http.Client{Transport: &http.Transport{DisableCompression: true}}).Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if got := res.Header.Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if got := res.Header.Get("Vary"); got != "Accept-Encoding" {
		t.Errorf("Vary = %q, want Accept-Encoding", got)
	}
	gz, err := gzip.NewReader(res.Body)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	var m map[string]any
	if err := json.NewDecoder(gz).Decode(&m); err != nil {
		t.Fatalf("decode gzipped body: %v", err)
	}
	if _, ok := m["rollups"]; !ok {
		t.Error("gzipped rollups payload missing rollups")
	}

	// A client that does not accept gzip gets identity.
	req2, err := http.NewRequest(http.MethodGet, ts.URL+"/api/rollups", nil)
	if err != nil {
		t.Fatal(err)
	}
	res2, err := (&http.Client{Transport: &http.Transport{DisableCompression: true}}).Do(req2)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = res2.Body.Close() }()
	if got := res2.Header.Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding without Accept-Encoding = %q, want none", got)
	}
	if err := json.NewDecoder(res2.Body).Decode(&m); err != nil {
		t.Fatalf("decode identity body: %v", err)
	}
}
