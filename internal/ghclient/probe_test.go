// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package ghclient

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
)

func TestProbeAlerts(t *testing.T) {
	tests := []struct {
		name       string
		kind       AlertKind
		status     int
		body       string
		wantStatus AccessStatus
		wantDetail string
	}{
		{"code scanning readable", AlertKindCodeScanning, http.StatusOK, `[]`, AccessOK, ""},
		{"secret scanning readable", AlertKindSecretScanning, http.StatusOK, `[]`, AccessOK, ""},
		{
			"advanced security disabled", AlertKindCodeScanning, http.StatusForbidden,
			`{"message":"Advanced Security must be enabled for this repository to use code scanning."}`,
			AccessForbidden, "Advanced Security must be enabled for this repository to use code scanning.",
		},
		{
			"missing permission", AlertKindCodeScanning, http.StatusForbidden,
			`{"message":"Resource not accessible by integration"}`,
			AccessForbidden, "Resource not accessible by integration",
		},
		{
			"no analysis", AlertKindCodeScanning, http.StatusNotFound,
			`{"message":"no analysis found"}`, AccessNoAnalysis, "no analysis found",
		},
		{
			"repository not visible", AlertKindCodeScanning, http.StatusNotFound,
			`{"message":"Not Found"}`, AccessNotFound, "Not Found",
		},
		{
			"secret scanning disabled", AlertKindSecretScanning, http.StatusNotFound,
			`{"message":"Secret scanning is disabled on this repository."}`,
			AccessNotFound, "Secret scanning is disabled on this repository.",
		},
		{
			"server error", AlertKindCodeScanning, http.StatusBadGateway,
			`{"message":"upstream"}`, AccessError, "upstream",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux, c := newFakeClient(t)
			path := "GET /repos/o/r/code-scanning/alerts"
			if tt.kind == AlertKindSecretScanning {
				path = "GET /repos/o/r/secret-scanning/alerts"
			}
			mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
				if got := r.URL.Query().Get("per_page"); got != "1" {
					t.Errorf("per_page = %q, want 1", got)
				}
				if got := r.URL.Query().Get("state"); got != "open" {
					t.Errorf("state = %q, want open", got)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})
			st, detail := c.ProbeAlerts(context.Background(), Repo{Owner: "o", Name: "r"}, tt.kind)
			if st != tt.wantStatus || detail != tt.wantDetail {
				t.Fatalf("ProbeAlerts() = (%q, %q), want (%q, %q)", st, detail, tt.wantStatus, tt.wantDetail)
			}
		})
	}
}

func TestClassifyInstallationNotFound(t *testing.T) {
	mux, app := newFakeApp(t)
	mux.HandleFunc("GET /repos/o/r/installation", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	})
	_, err := app.Installation(context.Background(), Repo{Owner: "o", Name: "r"})
	if st, _ := ClassifyAccess(err); st != AccessNotInstalled {
		t.Fatalf("ClassifyAccess(installation 404) = %q, want %q", st, AccessNotInstalled)
	}
}

func TestInstallationPermissions(t *testing.T) {
	mux, app := newFakeApp(t)
	var lookups atomic.Int32
	mux.HandleFunc("GET /repos/o/r/installation", func(w http.ResponseWriter, _ *http.Request) {
		lookups.Add(1)
		writeJSON(t, w, `{"id": 42, "permissions": {"contents": "write", "security_events": "read"}}`)
	})
	mux.HandleFunc("GET /repos/o/ro/installation", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, `{"id": 43, "permissions": {"metadata": "read"}}`)
	})
	ctx := context.Background()

	// The installation lookup Installation makes is reused.
	if _, err := app.Installation(ctx, Repo{Owner: "o", Name: "r"}); err != nil {
		t.Fatal(err)
	}
	got, err := app.InstallationPermissions(ctx, Repo{Owner: "o", Name: "r"})
	if err != nil || got != "write" {
		t.Fatalf("InstallationPermissions() = (%q, %v), want write", got, err)
	}
	if n := lookups.Load(); n != 1 {
		t.Fatalf("installation lookups = %d, want 1 (cached)", n)
	}

	got, err = app.InstallationPermissions(ctx, Repo{Owner: "o", Name: "ro"})
	if err != nil || got != "" {
		t.Fatalf("InstallationPermissions(no contents) = (%q, %v), want empty", got, err)
	}
}
