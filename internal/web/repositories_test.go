// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/ghclient"
	"github.com/bitwise-media-group/patchy/internal/repoaccess"
	"github.com/bitwise-media-group/patchy/internal/web/authz"
)

// fakeAccess answers the access check from a table; repositories absent
// from it are unreadable by every Integration.
type fakeAccess struct {
	readable map[string]string // repo → the Integration that can read it
	err      error
	asked    [][]string
}

func (f *fakeAccess) Enabled() bool { return true }

func (f *fakeAccess) Check(_ context.Context, repos []string) (map[string]repoaccess.Result, error) {
	f.asked = append(f.asked, slices.Clone(repos))
	if f.err != nil {
		return nil, f.err
	}
	out := map[string]repoaccess.Result{}
	for _, r := range repos {
		res := repoaccess.Result{Repo: r, Forge: &repoaccess.ForgeAccess{Status: repoaccess.ForgeMissing}}
		if name, ok := f.readable[r]; ok {
			res.Integrations = []repoaccess.IntegrationAccess{{
				Name:   name,
				Alerts: repoaccess.Alerts{CodeScanning: &repoaccess.Check{Status: repoaccess.StatusOK}},
			}}
		} else {
			res.Integrations = []repoaccess.IntegrationAccess{{
				Name: "gh",
				Alerts: repoaccess.Alerts{CodeScanning: &repoaccess.Check{
					Status: repoaccess.StatusNotInstalled, Detail: "Not Found",
				}},
			}}
		}
		out[r] = res
	}
	return out, nil
}

func repoFinding(name, repo string, phase v1alpha1.Phase) *v1alpha1.Finding {
	return &v1alpha1.Finding{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "patchy"},
		Spec: v1alpha1.FindingSpec{
			Title: name,
			Repository: &v1alpha1.FindingRepository{
				Type: v1alpha1.RepositoryTypeGitHub, URL: "https://github.com/" + repo, Name: repo,
			},
		},
		Status: v1alpha1.FindingStatus{Phase: phase},
	}
}

func scanningIntegration(name string, mutate ...func(*v1alpha1.Integration)) *v1alpha1.Integration {
	return testIntegration(name, append([]func(*v1alpha1.Integration){func(i *v1alpha1.Integration) {
		i.Spec.GitHub.CodeScanningAlerts = &v1alpha1.GitHubCodeScanningAlerts{Enabled: true}
	}}, mutate...)...)
}

func repoServer(t *testing.T, grants authz.Grants, access accessChecker, objs ...client.Object) *Server {
	t.Helper()
	s := testServer(t, objs...)
	s.auth, s.granter = stubAuth{id: operator}, stubGranter{grants: grants}
	if access != nil {
		s.WithRepositoryAccess(access)
	}
	return s
}

func doRequest(t *testing.T, s *Server, method, path, body string) (int, string) {
	t.Helper()
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	req, err := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, strings.TrimSpace(string(raw))
}

func repoParams(repos ...string) string {
	v := url.Values{}
	for _, r := range repos {
		v.Add("repo", r)
	}
	return v.Encode()
}

func TestTrackedRepositoriesCounts(t *testing.T) {
	s := repoServer(t, authz.Grants{View: true}, nil,
		repoFinding("f1", "acme/shop", v1alpha1.PhaseQueued),
		repoFinding("f2", "acme/shop", v1alpha1.PhaseQueued),
		repoFinding("f3", "acme/shop", v1alpha1.PhaseFailed),
		repoFinding("f4", "acme/shop", ""),
		repoFinding("f5", "acme/api", v1alpha1.PhaseRemediated),
		repoFinding("f6", "other/thing", v1alpha1.PhaseOpened),
		testRollup(v1alpha1.ScopeRepository, "acme/old", "repo-old"),
		testRollup(v1alpha1.ScopeModel, "acme/new", "model-shaped-like-a-repo"),
	)
	code, body := doRequest(t, s, http.MethodGet, "/api/repositories?"+
		repoParams("acme/shop", "acme/api", "acme/old", "acme/new", "acme/shop"), "")
	if code != http.StatusOK {
		t.Fatalf("status = %d: %s", code, body)
	}
	var out RepositoriesPayload
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if out.CanBackfill || out.AccessCheck {
		t.Errorf("canBackfill = %v, accessCheck = %v; want neither", out.CanBackfill, out.AccessCheck)
	}
	if len(out.Repositories) != 4 {
		t.Fatalf("repositories = %+v, want 4 (deduplicated, in request order)", out.Repositories)
	}
	shop, api, old, fresh := out.Repositories[0], out.Repositories[1], out.Repositories[2], out.Repositories[3]
	if shop.Name != "acme/shop" || shop.Findings != 4 || shop.Phases["Queued"] != 2 || shop.Phases["Failed"] != 1 ||
		len(shop.Phases) != 2 {
		t.Errorf("acme/shop = %+v", shop)
	}
	if api.Findings != 1 || api.Phases["Remediated"] != 1 || api.Rollup != nil {
		t.Errorf("acme/api = %+v", api)
	}
	if old.Findings != 0 || old.Rollup == nil || old.Rollup.Findings != 10 || old.Rollup.LastProcessed == "" {
		t.Errorf("acme/old = %+v, want the repository rollup", old)
	}
	// Only repository-scope shards count as a rollup.
	if fresh.Findings != 0 || fresh.Rollup != nil {
		t.Errorf("acme/new = %+v, want nothing", fresh)
	}
}

func TestTrackedRepositoriesValidation(t *testing.T) {
	many := make([]string, maxSummaryRepos+1)
	for i := range many {
		many[i] = "acme/r" + strings.Repeat("x", i+1)
	}
	cases := []struct {
		name   string
		grants authz.Grants
		query  string
		want   int
	}{
		{"no view grant", authz.Grants{Metrics: true}, repoParams("acme/shop"), http.StatusForbidden},
		{"owner prefix", authz.Grants{View: true}, repoParams("acme/"), http.StatusBadRequest},
		{"too many", authz.Grants{View: true}, repoParams(many...), http.StatusBadRequest},
		{"empty list", authz.Grants{View: true}, "", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := repoServer(t, tc.grants, nil)
			if code, body := doRequest(t, s, http.MethodGet, "/api/repositories?"+tc.query, ""); code != tc.want {
				t.Fatalf("status = %d (%s), want %d", code, body, tc.want)
			}
		})
	}
}

func TestTrackedRepositoriesBackfillState(t *testing.T) {
	at := metav1.NewTime(testClock.Add(-time.Hour))
	done := metav1.NewTime(testClock.Add(-time.Hour))
	pendingList := scanningIntegration("a-pending", func(i *v1alpha1.Integration) {
		i.Spec.Backfill = &v1alpha1.BackfillRequest{By: "op", At: at, Repositories: []string{"acme/shop"}}
	})
	finished := scanningIntegration("b-finished", func(i *v1alpha1.Integration) {
		i.Spec.Backfill = &v1alpha1.BackfillRequest{By: "op", At: at, Repositories: []string{"acme/", "x/y"}}
		i.Status.Backfill = &v1alpha1.BackfillStatus{
			BackfilledAt: &done, AttemptedAt: &done, LastRunAt: &done, Listed: 12, Ingested: 11,
		}
	})
	s := repoServer(t, authz.Grants{View: true, Integration: []string{authz.VerbBackfill}}, nil, pendingList, finished)
	code, body := doRequest(t, s, http.MethodGet, "/api/repositories?"+repoParams("acme/shop", "acme/api", "x/z"), "")
	if code != http.StatusOK {
		t.Fatalf("status = %d: %s", code, body)
	}
	var out RepositoriesPayload
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if !out.CanBackfill {
		t.Error("canBackfill = false with the backfill grant")
	}
	shop, api, xz := out.Repositories[0].Backfill, out.Repositories[1].Backfill, out.Repositories[2].Backfill
	// A pending request beats a finished one.
	if shop == nil || shop.Integration != "a-pending" || !shop.Pending {
		t.Errorf("acme/shop backfill = %+v, want pending on a-pending", shop)
	}
	// An owner prefix covers the repository.
	if api == nil || api.Integration != "b-finished" || api.Pending || api.Listed != 12 {
		t.Errorf("acme/api backfill = %+v, want finished on b-finished", api)
	}
	if xz != nil {
		t.Errorf("x/z backfill = %+v, want none", xz)
	}
}

func TestRepositoryAccess(t *testing.T) {
	access := &fakeAccess{readable: map[string]string{"acme/new": "gh"}}
	s := repoServer(t, authz.Grants{View: true}, access,
		repoFinding("f1", "acme/shop", v1alpha1.PhaseQueued),
		testRollup(v1alpha1.ScopeRepository, "acme/old", "repo-old"),
	)
	code, body := doRequest(t, s, http.MethodGet,
		"/api/repositories/access?"+repoParams("acme/shop", "acme/old", "acme/new", "acme/locked"), "")
	if code != http.StatusOK {
		t.Fatalf("status = %d: %s", code, body)
	}
	var out RepositoryAccessPayload
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	// Repositories patchy has seen are never probed.
	if len(access.asked) != 1 || !slices.Equal(access.asked[0], []string{"acme/new", "acme/locked"}) {
		t.Fatalf("probed %v, want only the never-seen repositories", access.asked)
	}
	if !out.Available || len(out.Repositories) != 4 {
		t.Fatalf("payload = %+v", out)
	}
	for _, i := range []int{0, 1} {
		if out.Repositories[i].Skipped != skippedKnown {
			t.Errorf("%s skipped = %q, want known", out.Repositories[i].Name, out.Repositories[i].Skipped)
		}
	}
	if got := out.Repositories[2]; got.BackfillIntegration != "gh" || got.Forge == nil {
		t.Errorf("acme/new = %+v, want backfillable through gh", got)
	}
	if got := out.Repositories[3]; got.BackfillIntegration != "" || len(got.Integrations) != 1 {
		t.Errorf("acme/locked = %+v, want unreadable", got)
	}
}

func TestRepositoryAccessUnavailable(t *testing.T) {
	t.Run("not configured", func(t *testing.T) {
		s := repoServer(t, authz.Grants{View: true}, nil)
		code, body := doRequest(t, s, http.MethodGet, "/api/repositories/access?"+repoParams("acme/new"), "")
		if code != http.StatusOK || !strings.Contains(body, `"available":false`) {
			t.Fatalf("status = %d, body = %s; want available false", code, body)
		}
	})
	t.Run("unreachable", func(t *testing.T) {
		s := repoServer(t, authz.Grants{View: true}, &fakeAccess{err: errors.New("dial tcp: refused")})
		code, body := doRequest(t, s, http.MethodGet, "/api/repositories/access?"+repoParams("acme/new"), "")
		var out RepositoryAccessPayload
		if err := json.Unmarshal([]byte(body), &out); err != nil || code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", code, body)
		}
		if !out.Available || out.Error == "" || strings.Contains(out.Error, "refused") {
			t.Fatalf("payload = %+v, want a caller-safe error", out)
		}
	})
	t.Run("backfill without a check", func(t *testing.T) {
		s := repoServer(t, allGrants(), nil, scanningIntegration("gh"))
		code, _ := doRequest(t, s, http.MethodPost, "/api/repositories/backfill", `{"repository":"acme/new"}`)
		if code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", code)
		}
	})
}

func TestRepositoryBackfill(t *testing.T) {
	at := metav1.NewTime(testClock.Add(-time.Minute))
	full := make([]string, maxBackfillRepos)
	for i := range full {
		full[i] = "acme/r" + strings.Repeat("x", i+1)
	}
	cases := []struct {
		name        string
		grants      authz.Grants
		body        string
		objs        []client.Object
		want        int
		wantOutcome string
		wantRepos   []string
		wantBy      string
	}{
		{
			name: "new request", grants: allGrants(), body: `{"repository":"acme/new"}`,
			objs: []client.Object{scanningIntegration("gh")},
			want: http.StatusOK, wantOutcome: "requested", wantRepos: []string{"acme/new"}, wantBy: "op@acme.test",
		},
		{
			name: "replaces a finished request", grants: allGrants(), body: `{"repository":"acme/new"}`,
			objs: []client.Object{scanningIntegration("gh", func(i *v1alpha1.Integration) {
				i.Spec.Backfill = &v1alpha1.BackfillRequest{By: "old", At: at, Repositories: []string{"x/"}}
				i.Status.Backfill = &v1alpha1.BackfillStatus{BackfilledAt: &at}
			})},
			want: http.StatusOK, wantOutcome: "requested", wantRepos: []string{"acme/new"}, wantBy: "op@acme.test",
		},
		{
			name: "merges into a pending list", grants: allGrants(), body: `{"repository":"acme/new"}`,
			objs: []client.Object{scanningIntegration("gh", func(i *v1alpha1.Integration) {
				i.Spec.Backfill = &v1alpha1.BackfillRequest{By: "other", At: at, Repositories: []string{"x/y"}}
			})},
			want: http.StatusOK, wantOutcome: "merged", wantRepos: []string{"x/y", "acme/new"}, wantBy: "other",
		},
		{
			name: "covered by a pending full scope", grants: allGrants(), body: `{"repository":"acme/new"}`,
			objs: []client.Object{scanningIntegration("gh", func(i *v1alpha1.Integration) {
				i.Spec.Backfill = &v1alpha1.BackfillRequest{By: "other", At: at}
			})},
			want: http.StatusOK, wantOutcome: "covered", wantBy: "other",
		},
		{
			name: "covered by a pending prefix", grants: allGrants(), body: `{"repository":"acme/new"}`,
			objs: []client.Object{scanningIntegration("gh", func(i *v1alpha1.Integration) {
				i.Spec.Backfill = &v1alpha1.BackfillRequest{By: "other", At: at, Repositories: []string{"acme/"}}
			})},
			want: http.StatusOK, wantOutcome: "covered", wantRepos: []string{"acme/"}, wantBy: "other",
		},
		{
			name: "pending list full", grants: allGrants(), body: `{"repository":"acme/new"}`,
			objs: []client.Object{scanningIntegration("gh", func(i *v1alpha1.Integration) {
				i.Spec.Backfill = &v1alpha1.BackfillRequest{By: "other", At: at, Repositories: full}
			})},
			want: http.StatusConflict,
		},
		{
			name: "no grant", grants: authz.Grants{View: true}, body: `{"repository":"acme/new"}`,
			objs: []client.Object{scanningIntegration("gh")}, want: http.StatusForbidden,
		},
		{
			name: "repository has findings", grants: allGrants(), body: `{"repository":"acme/shop"}`,
			objs: []client.Object{scanningIntegration("gh"), repoFinding("f1", "acme/shop", v1alpha1.PhaseRemediated)},
			want: http.StatusConflict,
		},
		{
			name: "repository has a rollup", grants: allGrants(), body: `{"repository":"acme/old"}`,
			objs: []client.Object{scanningIntegration("gh"), testRollup(v1alpha1.ScopeRepository, "acme/old", "r")},
			want: http.StatusConflict,
		},
		{
			name: "no integration can read it", grants: allGrants(), body: `{"repository":"acme/locked"}`,
			objs: []client.Object{scanningIntegration("gh")}, want: http.StatusConflict,
		},
		{
			name: "prefix entry", grants: allGrants(), body: `{"repository":"acme/"}`,
			objs: []client.Object{scanningIntegration("gh")}, want: http.StatusBadRequest,
		},
		{
			name: "malformed", grants: allGrants(), body: `{"repository":`,
			objs: []client.Object{scanningIntegration("gh")}, want: http.StatusBadRequest,
		},
		{
			name: "integration suspended", grants: allGrants(), body: `{"repository":"acme/new"}`,
			objs: []client.Object{scanningIntegration("gh", func(i *v1alpha1.Integration) { i.Spec.Suspend = true })},
			want: http.StatusConflict,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			access := &fakeAccess{readable: map[string]string{"acme/new": "gh", "acme/shop": "gh", "acme/old": "gh"}}
			s := repoServer(t, tc.grants, access, tc.objs...)
			code, body := doRequest(t, s, http.MethodPost, "/api/repositories/backfill", tc.body)
			if code != tc.want {
				t.Fatalf("status = %d (%s), want %d", code, body, tc.want)
			}
			if code == http.StatusConflict && strings.Contains(tc.name, "has") && len(access.asked) > 0 {
				t.Fatalf("a known repository reached the access check: %v", access.asked)
			}
			if tc.want != http.StatusOK {
				return
			}
			var res RepositoryBackfillResult
			if err := json.Unmarshal([]byte(body), &res); err != nil {
				t.Fatal(err)
			}
			if res.Integration != "gh" || res.Outcome != tc.wantOutcome {
				t.Fatalf("result = %+v, want gh/%s", res, tc.wantOutcome)
			}
			var integ v1alpha1.Integration
			if err := mustClient(s).Get(t.Context(), types.NamespacedName{Namespace: "patchy", Name: "gh"}, &integ); err != nil {
				t.Fatal(err)
			}
			req := integ.Spec.Backfill
			if req == nil || !slices.Equal(req.Repositories, tc.wantRepos) || req.By != tc.wantBy {
				t.Fatalf("spec.backfill = %+v, want repositories %v by %s", req, tc.wantRepos, tc.wantBy)
			}
			// A write moves the request time on, so the merged list is
			// pending again even if a walk of the old list is in flight.
			wantAt := testClock
			if tc.wantOutcome == "covered" {
				wantAt = at.Time
			}
			if !req.At.Time.Equal(wantAt) {
				t.Fatalf("spec.backfill.at = %v, want %v", req.At.Time, wantAt)
			}
		})
	}
}

func TestBackfillCovers(t *testing.T) {
	cases := []struct {
		filter []string
		repo   string
		want   bool
	}{
		{[]string{"acme/shop"}, "acme/shop", true},
		{[]string{"acme/"}, "acme/shop", true},
		{[]string{"acme"}, "acme/shop", true},
		{[]string{"acme"}, "acmex/shop", false},
		{[]string{"ACME/Shop"}, "acme/shop", true},
		// An entry is a prefix: the walk covers acme/shop-api too.
		{[]string{"acme/shop"}, "acme/shop-api", true},
		{[]string{"acme/sh"}, "acme/shop", true},
		{[]string{"acm/"}, "acme/shop", false},
		{[]string{"x/y"}, "acme/shop", false},
	}
	for _, tc := range cases {
		got := backfillCovers(tc.filter, tc.repo)
		if got != tc.want {
			t.Errorf("backfillCovers(%v, %s) = %v, want %v", tc.filter, tc.repo, got, tc.want)
		}
		// The page must agree with the walk the controller performs.
		owner, name, _ := strings.Cut(tc.repo, "/")
		if walk := ghclient.RepoMatches(tc.filter, ghclient.Repo{Owner: owner, Name: name}); walk != got {
			t.Errorf("backfillCovers(%v, %s) = %v, but the controller's walk says %v", tc.filter, tc.repo, got, walk)
		}
	}
}
