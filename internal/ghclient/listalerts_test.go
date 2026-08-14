// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package ghclient

import (
	"context"
	"net/http"
	"reflect"
	"testing"
)

func TestListRepoAlerts(t *testing.T) {
	mux, c := newFakeClient(t)
	mux.HandleFunc("GET /repos/o/r/code-scanning/alerts", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("state"); got != "open" {
			t.Errorf("state query = %q, want open", got)
		}
		pagedHandler(t,
			`[{"number":1,"rule":{"id":"go/a"}},{"number":2,"rule":{"id":"go/b"}}]`,
			`[{"number":3,"rule":{"id":"go/c"}}]`,
		)(w, r)
	})

	var nums []int
	complete, err := c.ListRepoAlerts(context.Background(), testRepo, func(a *Alert) bool {
		nums = append(nums, a.Number)
		return true
	})
	if err != nil {
		t.Fatalf("ListRepoAlerts() error = %v", err)
	}
	if !complete {
		t.Error("ListRepoAlerts() complete = false, want true")
	}
	if want := []int{1, 2, 3}; !reflect.DeepEqual(nums, want) {
		t.Errorf("ListRepoAlerts() yielded %v, want %v", nums, want)
	}
}

func TestListRepoAlertsEarlyStop(t *testing.T) {
	mux, c := newFakeClient(t)
	pages := 0
	mux.HandleFunc("GET /repos/o/r/code-scanning/alerts", func(w http.ResponseWriter, r *http.Request) {
		pages++
		pagedHandler(t,
			`[{"number":1},{"number":2}]`,
			`[{"number":3}]`,
		)(w, r)
	})

	var nums []int
	complete, err := c.ListRepoAlerts(context.Background(), testRepo, func(a *Alert) bool {
		nums = append(nums, a.Number)
		return false
	})
	if err != nil {
		t.Fatalf("ListRepoAlerts() error = %v", err)
	}
	if complete {
		t.Error("ListRepoAlerts() complete = true after early stop, want false")
	}
	if want := []int{1}; !reflect.DeepEqual(nums, want) {
		t.Errorf("ListRepoAlerts() yielded %v, want %v", nums, want)
	}
	if pages != 1 {
		t.Errorf("early stop fetched %d pages, want 1", pages)
	}
}

func TestListOrgAlerts(t *testing.T) {
	mux, c := newFakeClient(t)
	mux.HandleFunc("GET /orgs/acme/code-scanning/alerts", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("state"); got != "open" {
			t.Errorf("state query = %q, want open", got)
		}
		pagedHandler(t,
			`[{"number":1,"repository":{"name":"r1","owner":{"login":"acme"}}}]`,
			`[{"number":2,"repository":{"name":"r2","owner":{"login":"acme"}}}]`,
		)(w, r)
	})

	type hit struct {
		repo Repo
		num  int
	}
	var hits []hit
	complete, err := c.ListOrgAlerts(context.Background(), "acme", func(r Repo, a *Alert) bool {
		hits = append(hits, hit{r, a.Number})
		return true
	})
	if err != nil {
		t.Fatalf("ListOrgAlerts() error = %v", err)
	}
	if !complete {
		t.Error("ListOrgAlerts() complete = false, want true")
	}
	want := []hit{
		{Repo{Owner: "acme", Name: "r1"}, 1},
		{Repo{Owner: "acme", Name: "r2"}, 2},
	}
	if !reflect.DeepEqual(hits, want) {
		t.Errorf("ListOrgAlerts() yielded %v, want %v", hits, want)
	}
}

// installationTokens registers the access-token endpoints the enumerator's
// installation clients mint through.
func installationTokens(t *testing.T, mux *http.ServeMux, ids ...string) {
	t.Helper()
	for _, id := range ids {
		mux.HandleFunc("POST /app/installations/"+id+"/access_tokens",
			func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, `{"token":"inst-tok","expires_at":"2100-01-01T00:00:00Z"}`)
			})
	}
}

func TestAppAlertEnumerator(t *testing.T) {
	mux, app := newFakeApp(t)
	mux.HandleFunc("GET /app/installations", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, `[
			{"id":11,"account":{"login":"acme"},"target_type":"Organization"},
			{"id":22,"account":{"login":"bob"},"target_type":"User"}
		]`)
	})
	installationTokens(t, mux, "11", "22")
	mux.HandleFunc("GET /orgs/acme/code-scanning/alerts", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, `[{"number":1,"repository":{"name":"r1","owner":{"login":"acme"}}}]`)
	})
	mux.HandleFunc("GET /installation/repositories", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, `{"repositories":[{"name":"toy","owner":{"login":"bob"}}]}`)
	})
	mux.HandleFunc("GET /repos/bob/toy/code-scanning/alerts", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, `[{"number":7}]`)
	})

	e := &AppAlertEnumerator{App: app}
	var got []string
	complete, err := e.Enumerate(context.Background(), nil, func(r Repo, a *Alert) bool {
		got = append(got, r.String())
		return true
	})
	if err != nil {
		t.Fatalf("Enumerate() error = %v", err)
	}
	if !complete {
		t.Error("Enumerate() complete = false, want true")
	}
	if want := []string{"acme/r1", "bob/toy"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Enumerate() yielded %v, want %v", got, want)
	}
}

// A prefix filter skips whole installations whose account no entry names —
// the user installation's endpoints must never be hit.
func TestAppAlertEnumeratorPrefixSkip(t *testing.T) {
	mux, app := newFakeApp(t)
	mux.HandleFunc("GET /app/installations", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, `[
			{"id":11,"account":{"login":"acme"},"target_type":"Organization"},
			{"id":22,"account":{"login":"bob"},"target_type":"User"}
		]`)
	})
	installationTokens(t, mux, "11")
	mux.HandleFunc("GET /orgs/acme/code-scanning/alerts", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, `[{"number":1,"repository":{"name":"r1","owner":{"login":"acme"}}}]`)
	})
	mux.HandleFunc("GET /installation/repositories", func(w http.ResponseWriter, _ *http.Request) {
		t.Error("skipped installation was enumerated")
	})

	e := &AppAlertEnumerator{App: app}
	var got []string
	complete, err := e.Enumerate(context.Background(), []string{"ACME/"}, func(r Repo, a *Alert) bool {
		got = append(got, r.String())
		return true
	})
	if err != nil {
		t.Fatalf("Enumerate() error = %v", err)
	}
	if !complete {
		t.Error("Enumerate() complete = false, want true")
	}
	if want := []string{"acme/r1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Enumerate() yielded %v, want %v", got, want)
	}
}

// Entries that all carry a name segment switch an org account to
// repo-enumeration mode: the repository inventory is listed, only
// matching repositories are walked, and the org-wide alert listing —
// whose cost scales with the org's alert count — is never touched.
func TestAppAlertEnumeratorNamePrefixMode(t *testing.T) {
	mux, app := newFakeApp(t)
	mux.HandleFunc("GET /app/installations", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, `[{"id":11,"account":{"login":"acme"},"target_type":"Organization"}]`)
	})
	installationTokens(t, mux, "11")
	mux.HandleFunc("GET /orgs/acme/code-scanning/alerts", func(w http.ResponseWriter, _ *http.Request) {
		t.Error("name-prefix filter fell back to the org-wide alert walk")
	})
	mux.HandleFunc("GET /installation/repositories", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, `{"repositories":[
			{"name":"svc-a","owner":{"login":"acme"}},
			{"name":"svc-b","owner":{"login":"acme"}},
			{"name":"web","owner":{"login":"acme"}}
		]}`)
	})
	mux.HandleFunc("GET /repos/acme/svc-a/code-scanning/alerts", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, `[{"number":1}]`)
	})
	mux.HandleFunc("GET /repos/acme/svc-b/code-scanning/alerts", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, `[{"number":2}]`)
	})
	mux.HandleFunc("GET /repos/acme/web/code-scanning/alerts", func(w http.ResponseWriter, _ *http.Request) {
		t.Error("non-matching repository was walked")
	})

	e := &AppAlertEnumerator{App: app}
	var got []string
	complete, err := e.Enumerate(context.Background(), []string{"acme/svc-"}, func(r Repo, a *Alert) bool {
		got = append(got, r.String())
		return true
	})
	if err != nil {
		t.Fatalf("Enumerate() error = %v", err)
	}
	if !complete {
		t.Error("Enumerate() complete = false, want true")
	}
	if want := []string{"acme/svc-a", "acme/svc-b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Enumerate() yielded %v, want %v", got, want)
	}
}

// A bare owner entry alongside a name prefix keeps the full walk — the
// bare entry already covers the whole account.
func TestAppAlertEnumeratorBareOwnerKeepsOrgWalk(t *testing.T) {
	mux, app := newFakeApp(t)
	mux.HandleFunc("GET /app/installations", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, `[{"id":11,"account":{"login":"acme"},"target_type":"Organization"}]`)
	})
	installationTokens(t, mux, "11")
	mux.HandleFunc("GET /orgs/acme/code-scanning/alerts", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, `[{"number":1,"repository":{"name":"r1","owner":{"login":"acme"}}}]`)
	})
	mux.HandleFunc("GET /installation/repositories", func(w http.ResponseWriter, _ *http.Request) {
		t.Error("bare-owner filter enumerated the repository inventory")
	})

	e := &AppAlertEnumerator{App: app}
	complete, err := e.Enumerate(context.Background(), []string{"acme/", "acme/svc-"},
		func(Repo, *Alert) bool { return true })
	if err != nil {
		t.Fatalf("Enumerate() error = %v", err)
	}
	if !complete {
		t.Error("Enumerate() complete = false, want true")
	}
}

func TestAppAlertEnumeratorBudget(t *testing.T) {
	mux, app := newFakeApp(t)
	mux.HandleFunc("GET /app/installations", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, `[{"id":11,"account":{"login":"acme"},"target_type":"Organization"}]`)
	})
	installationTokens(t, mux, "11")
	mux.HandleFunc("GET /orgs/acme/code-scanning/alerts", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, `[
			{"number":1,"repository":{"name":"r1","owner":{"login":"acme"}}},
			{"number":2,"repository":{"name":"r1","owner":{"login":"acme"}}},
			{"number":3,"repository":{"name":"r1","owner":{"login":"acme"}}}
		]`)
	})

	e := &AppAlertEnumerator{App: app, MaxAlerts: 2}
	var got []int
	complete, err := e.Enumerate(context.Background(), nil, func(_ Repo, a *Alert) bool {
		got = append(got, a.Number)
		return true
	})
	if err != nil {
		t.Fatalf("Enumerate() error = %v", err)
	}
	if complete {
		t.Error("Enumerate() complete = true over budget, want false")
	}
	if want := []int{1, 2}; !reflect.DeepEqual(got, want) {
		t.Errorf("Enumerate() yielded %v, want %v", got, want)
	}
}

func TestPATAlertEnumerator(t *testing.T) {
	mux, c := newFakeClient(t)
	mux.HandleFunc("GET /repos/o/r/code-scanning/alerts", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, `[{"number":5}]`)
	})

	e := &PATAlertEnumerator{Client: c}
	var got []int
	complete, err := e.Enumerate(context.Background(), []string{"o/r"}, func(r Repo, a *Alert) bool {
		if r != testRepo {
			t.Errorf("yielded repo = %v, want %v", r, testRepo)
		}
		got = append(got, a.Number)
		return true
	})
	if err != nil {
		t.Fatalf("Enumerate() error = %v", err)
	}
	if !complete {
		t.Error("Enumerate() complete = false, want true")
	}
	if want := []int{5}; !reflect.DeepEqual(got, want) {
		t.Errorf("Enumerate() yielded %v, want %v", got, want)
	}
}

// A PAT cannot discover repositories: an empty filter and a bare prefix
// are both errors, not empty results.
func TestPATAlertEnumeratorRejectsPrefixes(t *testing.T) {
	_, c := newFakeClient(t)
	e := &PATAlertEnumerator{Client: c}
	for _, repos := range [][]string{nil, {"o/"}, {"o"}} {
		if _, err := e.Enumerate(context.Background(), repos,
			func(Repo, *Alert) bool { return true }); err == nil {
			t.Errorf("Enumerate(%v) error = nil, want non-nil", repos)
		}
	}
}

func TestRepoMatches(t *testing.T) {
	repo := Repo{Owner: "acme", Name: "shop"}
	tests := []struct {
		name    string
		entries []string
		want    bool
	}{
		{"empty filter matches everything", nil, true},
		{"owner prefix", []string{"acme/"}, true},
		{"slash-less owner", []string{"acme"}, true},
		{"exact repo", []string{"acme/shop"}, true},
		{"name prefix", []string{"acme/sh"}, true},
		{"case-insensitive", []string{"ACME/Shop"}, true},
		{"other owner", []string{"beta/"}, false},
		{"owner is whole-segment, not substring", []string{"acmex/"}, false},
		{"slash-less owner is not a substring", []string{"acm"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RepoMatches(tt.entries, repo); got != tt.want {
				t.Errorf("RepoMatches(%v, %v) = %v, want %v", tt.entries, repo, got, tt.want)
			}
		})
	}
}

func TestFilterHelpers(t *testing.T) {
	repos := []string{"acme/", "Acme/svc-", "beta/app", "gamma"}
	if got := entriesForOwner(repos, "ACME"); !reflect.DeepEqual(got, []string{"acme/", "Acme/svc-"}) {
		t.Errorf("entriesForOwner() = %v, want the two acme entries", got)
	}
	if got := entriesForOwner(repos, "delta"); got != nil {
		t.Errorf("entriesForOwner(delta) = %v, want nil", got)
	}
	tests := []struct {
		entries []string
		want    bool
	}{
		{[]string{"acme/"}, true},
		{[]string{"acme"}, true},
		{[]string{"acme/svc-"}, false},
		{[]string{"acme/shop", "acme/"}, true},
		{[]string{"acme/shop", "acme/svc-"}, false},
	}
	for _, tt := range tests {
		if got := hasBareOwner(tt.entries); got != tt.want {
			t.Errorf("hasBareOwner(%v) = %v, want %v", tt.entries, got, tt.want)
		}
	}
}
