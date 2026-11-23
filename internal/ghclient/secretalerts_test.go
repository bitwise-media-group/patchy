// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package ghclient

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// fakeSecretValue is what the fake server returns in every "secret" field.
// It must never surface in anything the client hands back.
const fakeSecretValue = "patchy-test-not-a-real-credential"

// requireHidden fails the request unless it asked GitHub to hide the
// secret, the way the e2e fake does.
func requireHidden(t *testing.T, w http.ResponseWriter, r *http.Request) bool {
	t.Helper()
	if r.URL.Query().Get("hide_secret") != "true" {
		t.Errorf("%s %s without hide_secret=true", r.Method, r.URL)
		http.Error(w, "hide_secret required", http.StatusBadRequest)
		return false
	}
	return true
}

// assertNoSecret fails when the value appears anywhere in v's rendering.
func assertNoSecret(t *testing.T, v any) {
	t.Helper()
	if s := fmt.Sprintf("%+v", v); strings.Contains(s, fakeSecretValue) {
		t.Errorf("secret value leaked into %s", s)
	}
}

func TestGetSecretAlert(t *testing.T) {
	mux, c := newFakeClient(t)
	mux.HandleFunc("GET /repos/o/r/secret-scanning/alerts/3", func(w http.ResponseWriter, r *http.Request) {
		if !requireHidden(t, w, r) {
			return
		}
		writeJSON(t, w, `{
			"number":3,"state":"open","html_url":"https://gh/o/r/security/secret-scanning/3",
			"secret_type":"github_personal_access_token",
			"secret_type_display_name":"GitHub Personal Access Token",
			"provider":"GitHub","validity":"active","publicly_leaked":true,"multi_repo":false,
			"push_protection_bypassed":true,"secret":"`+fakeSecretValue+`"
		}`)
	})
	mux.HandleFunc("GET /repos/o/r/secret-scanning/alerts/3/locations", func(w http.ResponseWriter, r *http.Request) {
		if !requireHidden(t, w, r) {
			return
		}
		writeJSON(t, w, `[
			{"type":"commit","details":{"path":"config/app.yaml","start_line":4,"end_line":4,
				"start_column":9,"end_column":49,"commit_sha":"abc","blob_url":"https://api/blob"}},
			{"type":"issue_body","details":{"issue_body_url":"https://api/issues/9"}}
		]`)
	})

	got, err := c.GetSecretAlert(context.Background(), testRepo, 3)
	if err != nil {
		t.Fatalf("GetSecretAlert() error = %v", err)
	}
	want := &SecretAlert{
		Number: 3, SecretType: "github_personal_access_token",
		SecretTypeDisplayName: "GitHub Personal Access Token", Provider: "GitHub",
		Validity: "active", PubliclyLeaked: true, PushProtectionBypassed: true,
		State: "open", HTMLURL: "https://gh/o/r/security/secret-scanning/3",
		Locations: []SecretLocation{
			{
				Type: "commit", Path: "config/app.yaml", StartLine: 4, EndLine: 4,
				StartColumn: 9, EndColumn: 49, CommitSHA: "abc", URL: "https://api/blob",
			},
			{Type: "issue_body", URL: "https://api/issues/9"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("GetSecretAlert() = %+v, want %+v", got, want)
	}
	assertNoSecret(t, got)
}

// A list response carries no locations; the first one detected stands in.
func TestGetSecretAlertFallsBackToFirstLocation(t *testing.T) {
	mux, c := newFakeClient(t)
	mux.HandleFunc("GET /repos/o/r/secret-scanning/alerts/3", func(w http.ResponseWriter, r *http.Request) {
		if !requireHidden(t, w, r) {
			return
		}
		writeJSON(t, w, `{"number":3,"secret_type":"x",
			"first_location_detected":{"path":"a.env","start_line":2,"end_line":2}}`)
	})
	mux.HandleFunc("GET /repos/o/r/secret-scanning/alerts/3/locations", func(w http.ResponseWriter, r *http.Request) {
		if requireHidden(t, w, r) {
			writeJSON(t, w, `[]`)
		}
	})
	got, err := c.GetSecretAlert(context.Background(), testRepo, 3)
	if err != nil {
		t.Fatalf("GetSecretAlert() error = %v", err)
	}
	want := []SecretLocation{{Type: "commit", Path: "a.env", StartLine: 2, EndLine: 2}}
	if !reflect.DeepEqual(got.Locations, want) {
		t.Errorf("Locations = %+v, want %+v", got.Locations, want)
	}
}

func TestResolveSecretAlert(t *testing.T) {
	mux, c := newFakeClient(t)
	mux.HandleFunc("PATCH /repos/o/r/secret-scanning/alerts/3", func(w http.ResponseWriter, r *http.Request) {
		want := map[string]any{
			"state":              "resolved",
			"resolution":         "used_in_tests",
			"resolution_comment": "test fixture",
		}
		if body := decodeBody[map[string]any](t, r); !reflect.DeepEqual(body, want) {
			t.Errorf("resolve request = %v, want %v", body, want)
		}
		writeJSON(t, w, `{"number":3,"state":"resolved","secret":"`+fakeSecretValue+`"}`)
	})
	if err := c.ResolveSecretAlert(context.Background(), testRepo, 3, "used_in_tests", "test fixture"); err != nil {
		t.Fatalf("ResolveSecretAlert() error = %v", err)
	}
}

func TestReopenSecretAlert(t *testing.T) {
	tests := []struct {
		name      string
		getStatus int
		state     string
		wantPatch bool
	}{
		{name: "resolved alert reopens", getStatus: http.StatusOK, state: "resolved", wantPatch: true},
		{name: "open alert is left alone", getStatus: http.StatusOK, state: "open"},
		{name: "missing alert is success", getStatus: http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux, c := newFakeClient(t)
			mux.HandleFunc("GET /repos/o/r/secret-scanning/alerts/3", func(w http.ResponseWriter, r *http.Request) {
				if !requireHidden(t, w, r) {
					return
				}
				if tt.getStatus != http.StatusOK {
					http.Error(w, `{"message":"Not Found"}`, tt.getStatus)
					return
				}
				writeJSON(t, w, `{"number":3,"state":"`+tt.state+`"}`)
			})
			patched := false
			mux.HandleFunc("PATCH /repos/o/r/secret-scanning/alerts/3", func(w http.ResponseWriter, r *http.Request) {
				patched = true
				if body := decodeBody[map[string]any](t, r); body["state"] != "open" {
					t.Errorf("reopen request = %v, want state open", body)
				}
				writeJSON(t, w, `{"number":3,"state":"open"}`)
			})
			if err := c.ReopenSecretAlert(context.Background(), testRepo, 3); err != nil {
				t.Fatalf("ReopenSecretAlert() error = %v", err)
			}
			if patched != tt.wantPatch {
				t.Errorf("patched = %v, want %v", patched, tt.wantPatch)
			}
		})
	}
}

// The listing follows the after cursor GitHub puts in the Link header and
// asks for open alerts with the secret hidden on every page.
func TestListRepoSecretAlertsCursorPagination(t *testing.T) {
	mux, c := newFakeClient(t)
	mux.HandleFunc("GET /repos/o/r/secret-scanning/alerts", func(w http.ResponseWriter, r *http.Request) {
		if !requireHidden(t, w, r) {
			return
		}
		if got := r.URL.Query().Get("state"); got != "open" {
			t.Errorf("state = %q, want open", got)
		}
		if r.URL.Query().Get("after") == "CUR2" {
			writeJSON(t, w, `[{"number":2,"secret":"`+fakeSecretValue+`"}]`)
			return
		}
		w.Header().Set("Link", `<`+r.URL.Path+`?after=CUR2>; rel="next"`)
		writeJSON(t, w, `[{"number":1,"secret":"`+fakeSecretValue+`"}]`)
	})
	var got []int
	complete, err := c.ListRepoSecretAlerts(context.Background(), testRepo, func(a *SecretAlert) bool {
		assertNoSecret(t, a)
		got = append(got, a.Number)
		return true
	})
	if err != nil {
		t.Fatalf("ListRepoSecretAlerts() error = %v", err)
	}
	if !complete {
		t.Error("complete = false, want true")
	}
	if want := []int{1, 2}; !reflect.DeepEqual(got, want) {
		t.Errorf("listed %v, want %v", got, want)
	}
}

func TestAppSecretAlertEnumerator(t *testing.T) {
	mux, app := newFakeApp(t)
	mux.HandleFunc("GET /app/installations", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, `[
			{"id":11,"account":{"login":"acme"},"target_type":"Organization"},
			{"id":22,"account":{"login":"bob"},"target_type":"User"}
		]`)
	})
	installationTokens(t, mux, "11", "22")
	mux.HandleFunc("GET /orgs/acme/secret-scanning/alerts", func(w http.ResponseWriter, r *http.Request) {
		if requireHidden(t, w, r) {
			writeJSON(t, w, `[{"number":1,"repository":{"name":"r1","owner":{"login":"acme"}}}]`)
		}
	})
	mux.HandleFunc("GET /installation/repositories", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, `{"repositories":[{"name":"toy","owner":{"login":"bob"}}]}`)
	})
	mux.HandleFunc("GET /repos/bob/toy/secret-scanning/alerts", func(w http.ResponseWriter, r *http.Request) {
		if requireHidden(t, w, r) {
			writeJSON(t, w, `[{"number":7}]`)
		}
	})

	e := &AppSecretAlertEnumerator{App: app}
	var got []string
	complete, err := e.Enumerate(context.Background(), nil, func(r Repo, a *SecretAlert) bool {
		got = append(got, fmt.Sprintf("%s#%d", r, a.Number))
		return true
	})
	if err != nil {
		t.Fatalf("Enumerate() error = %v", err)
	}
	if !complete {
		t.Error("Enumerate() complete = false, want true")
	}
	if want := []string{"acme/r1#1", "bob/toy#7"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Enumerate() yielded %v, want %v", got, want)
	}
}

func TestPATSecretAlertEnumerator(t *testing.T) {
	mux, c := newFakeClient(t)
	mux.HandleFunc("GET /repos/o/r/secret-scanning/alerts", func(w http.ResponseWriter, r *http.Request) {
		if requireHidden(t, w, r) {
			writeJSON(t, w, `[{"number":1},{"number":2}]`)
		}
	})
	e := &PATSecretAlertEnumerator{Client: c, MaxAlerts: 1}
	var got []int
	complete, err := e.Enumerate(context.Background(), []string{"o/r"}, func(_ Repo, a *SecretAlert) bool {
		got = append(got, a.Number)
		return true
	})
	if err != nil {
		t.Fatalf("Enumerate() error = %v", err)
	}
	if complete {
		t.Error("complete = true, want false at the budget")
	}
	if want := []int{1}; !reflect.DeepEqual(got, want) {
		t.Errorf("listed %v, want %v", got, want)
	}
	all := func(Repo, *SecretAlert) bool { return true }
	if _, err := e.Enumerate(context.Background(), []string{"o/"}, all); err == nil {
		t.Error("Enumerate(prefix) error = nil, want a PAT prefix error")
	}
}
