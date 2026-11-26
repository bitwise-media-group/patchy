// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	done := metav1.NewTime(testClock.Add(-time.Hour))
	old := metav1.NewTime(testClock.Add(-48 * time.Hour))
	objs := []client.Object{
		// acme/shop: an open request beats a newer finished one.
		testBackfillRequest("shop-open", -2*time.Hour, func(b *v1alpha1.BackfillRequest) {
			b.Spec.Repositories = []string{"acme/shop"}
			b.Spec.Via = v1alpha1.BackfillViaRequested
		}),
		testBackfillRequest("shop-done", -time.Hour, func(b *v1alpha1.BackfillRequest) {
			b.Spec.Repositories = []string{"acme/shop"}
			b.Status = v1alpha1.BackfillRequestStatus{Phase: v1alpha1.BackfillComplete, CompletedAt: &done, Attempts: 1}
		}),
		// acme/api: an owner prefix covers it, finished an hour ago: inside
		// the cooldown.
		testBackfillRequest("acme-prefix", -90*time.Minute, func(b *v1alpha1.BackfillRequest) {
			b.Spec.Repositories = []string{"acme/", "x/y"}
			b.Status = v1alpha1.BackfillRequestStatus{
				Phase: v1alpha1.BackfillComplete, CompletedAt: &done, LastAttemptAt: &done, Attempts: 1, Listed: 12, Ingested: 11,
			}
		}),
		// x/old: finished two days ago: outside the cooldown.
		testBackfillRequest("x-old", -49*time.Hour, func(b *v1alpha1.BackfillRequest) {
			b.Spec.Repositories = []string{"x/old"}
			b.Status = v1alpha1.BackfillRequestStatus{Phase: v1alpha1.BackfillComplete, CompletedAt: &old, Attempts: 1}
		}),
		// x/denied: an operator said no.
		testBackfillRequest("x-denied", -time.Hour, func(b *v1alpha1.BackfillRequest) {
			b.Spec.Repositories = []string{"x/denied"}
			b.Spec.Via = v1alpha1.BackfillViaRequested
			b.Spec.Decision = &v1alpha1.BackfillDecision{Verdict: v1alpha1.BackfillDeniedVerdict, By: "ops", At: done,
				Reason: "already scanned"}
			b.Status = v1alpha1.BackfillRequestStatus{Phase: v1alpha1.BackfillDenied, CompletedAt: &done}
		}),
		// A finished whole-scope walk says nothing about x/z.
		testBackfillRequest("whole", -time.Hour, func(b *v1alpha1.BackfillRequest) {
			b.Status = v1alpha1.BackfillRequestStatus{Phase: v1alpha1.BackfillComplete, CompletedAt: &done}
		}),
	}
	s := repoServer(t, authz.Grants{View: true, Integration: []string{authz.VerbBackfill}}, nil, objs...)
	code, body := doRequest(t, s, http.MethodGet, "/api/repositories?"+
		repoParams("acme/shop", "acme/api", "x/old", "x/denied", "x/z"), "")
	if code != http.StatusOK {
		t.Fatalf("status = %d: %s", code, body)
	}
	var out RepositoriesPayload
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if !out.CanBackfill || !out.CanRequestBackfill || out.CanBackfillMaintained ||
		out.CooldownSeconds != int64(DefaultBackfillCooldown.Seconds()) {
		t.Errorf("payload flags = %+v", out)
	}
	checkBackfillRows(t, out.Repositories, done.Add(DefaultBackfillCooldown).UTC().Format(time.RFC3339))
}

// checkBackfillRows asserts TestTrackedRepositoriesBackfillState's rows.
func checkBackfillRows(t *testing.T, r []RepositorySummary, wantAvail string) {
	t.Helper()
	if b := r[0].Backfill; b == nil || b.Request != "shop-open" || !b.Pending || b.Phase != "AwaitingApproval" ||
		b.Via != "requested" {
		t.Errorf("acme/shop backfill = %+v, want the open request awaiting approval", b)
	}
	if b := r[1].Backfill; b == nil || b.Request != "acme-prefix" || b.Pending || b.Listed != 12 {
		t.Errorf("acme/api backfill = %+v, want the finished prefix walk", b)
	}
	if r[1].BackfillAvailableAt != wantAvail || r[0].BackfillAvailableAt != wantAvail {
		t.Errorf("availableAt = %q / %q, want %q (inside the cooldown)", r[0].BackfillAvailableAt,
			r[1].BackfillAvailableAt, wantAvail)
	}
	if r[2].BackfillAvailableAt != "" || r[2].Backfill == nil {
		t.Errorf("x/old = %+v, want backfillable again", r[2])
	}
	if b := r[3].Backfill; b == nil || b.Phase != "Denied" || b.DecisionReason != "already scanned" {
		t.Errorf("x/denied backfill = %+v, want the denial and its reason", b)
	}
	if r[4].Backfill != nil || r[4].BackfillAvailableAt != "" {
		t.Errorf("x/z = %+v, want nothing", r[4])
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
	recent := metav1.NewTime(testClock.Add(-time.Hour))
	stale := metav1.NewTime(testClock.Add(-25 * time.Hour))
	finished := func(name, repo string, at metav1.Time) *v1alpha1.BackfillRequest {
		return testBackfillRequest(name, at.Sub(testClock), func(b *v1alpha1.BackfillRequest) {
			b.Spec.Repositories = []string{repo}
			b.Status = v1alpha1.BackfillRequestStatus{Phase: v1alpha1.BackfillComplete, CompletedAt: &at, Attempts: 1}
		})
	}
	viewer := authz.Grants{View: true}
	cases := []struct {
		name        string
		grants      authz.Grants
		body        string
		objs        []client.Object
		want        int
		wantOutcome string
		wantVia     v1alpha1.BackfillVia
		wantCreated bool
	}{
		{
			name: "granted: runs at once", grants: allGrants(), body: `{"repository":"acme/new"}`,
			objs: []client.Object{scanningIntegration("gh")},
			want: http.StatusOK, wantOutcome: outcomeRequested, wantCreated: true,
		},
		{
			// Patchy has seen it, and that no longer matters: only the
			// cooldown does.
			name: "a known repository can be backfilled again", grants: allGrants(), body: `{"repository":"acme/shop"}`,
			objs: []client.Object{scanningIntegration("gh"), repoFinding("f1", "acme/shop", v1alpha1.PhaseRemediated),
				finished("old", "acme/shop", stale)},
			want: http.StatusOK, wantOutcome: outcomeRequested, wantCreated: true,
		},
		{
			name: "inside the cooldown", grants: allGrants(), body: `{"repository":"acme/shop"}`,
			objs: []client.Object{scanningIntegration("gh"), finished("recent", "acme/shop", recent)},
			want: http.StatusTooManyRequests,
		},
		{
			name: "inside the cooldown through a prefix walk", grants: allGrants(), body: `{"repository":"acme/shop"}`,
			objs: []client.Object{scanningIntegration("gh"), finished("recent", "acme/", recent)},
			want: http.StatusTooManyRequests,
		},
		{
			name: "covered by an open request", grants: allGrants(), body: `{"repository":"acme/new"}`,
			objs: []client.Object{scanningIntegration("gh"), testBackfillRequest("open", -time.Minute,
				func(b *v1alpha1.BackfillRequest) { b.Spec.Repositories = []string{"acme/"} })},
			want: http.StatusOK, wantOutcome: outcomeCovered,
		},
		{
			name: "covered by an open whole-scope request", grants: viewer, body: `{"repository":"acme/new","request":true}`,
			objs: []client.Object{scanningIntegration("gh"), testBackfillRequest("open", -time.Minute)},
			want: http.StatusOK, wantOutcome: outcomeCovered,
		},
		{
			name: "no grant: told to request", grants: viewer, body: `{"repository":"acme/new"}`,
			objs: []client.Object{scanningIntegration("gh")}, want: http.StatusForbidden,
		},
		{
			name: "viewer files a request", grants: viewer, body: `{"repository":"acme/new","request":true}`,
			objs: []client.Object{scanningIntegration("gh")},
			want: http.StatusOK, wantOutcome: outcomeAwaitingApproval, wantVia: v1alpha1.BackfillViaRequested,
			wantCreated: true,
		},
		{
			name: "a request needs the view grant", grants: authz.Grants{Metrics: true},
			body: `{"repository":"acme/new","request":true}`,
			objs: []client.Object{scanningIntegration("gh")}, want: http.StatusForbidden,
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
			access := &fakeAccess{readable: map[string]string{"acme/new": "gh", "acme/shop": "gh"}}
			s := repoServer(t, tc.grants, access, tc.objs...)
			before := countRequests(t, s)
			code, body := doRequest(t, s, http.MethodPost, "/api/repositories/backfill", tc.body)
			if code != tc.want {
				t.Fatalf("status = %d (%s), want %d", code, body, tc.want)
			}
			created := countRequests(t, s) - before
			if created != map[bool]int{true: 1}[tc.wantCreated] {
				t.Fatalf("requests created = %d, want created %v", created, tc.wantCreated)
			}
			if tc.want != http.StatusOK {
				return
			}
			var res RepositoryBackfillResult
			if err := json.Unmarshal([]byte(body), &res); err != nil {
				t.Fatal(err)
			}
			if res.Integration != "gh" || res.Outcome != tc.wantOutcome || res.Request == "" {
				t.Fatalf("result = %+v, want gh/%s", res, tc.wantOutcome)
			}
			if !tc.wantCreated {
				return
			}
			var bfr v1alpha1.BackfillRequest
			key := types.NamespacedName{Namespace: "patchy", Name: res.Request}
			if err := mustClient(s).Get(t.Context(), key, &bfr); err != nil {
				t.Fatal(err)
			}
			if bfr.Spec.RequestedBy != "op@acme.test" || bfr.Spec.Via != tc.wantVia || len(bfr.Spec.Repositories) != 1 {
				t.Fatalf("request spec = %+v", bfr.Spec)
			}
		})
	}
}

func TestRepositoryBackfillOpenRequestCap(t *testing.T) {
	objs := make([]client.Object, 0, 1+maxOpenRequestsPerUser)
	objs = append(objs, scanningIntegration("gh"))
	for i := range maxOpenRequestsPerUser {
		objs = append(objs, testBackfillRequest(fmt.Sprintf("open-%d", i), -time.Minute, func(b *v1alpha1.BackfillRequest) {
			b.Spec.RequestedBy = "op@acme.test"
			b.Spec.Via = v1alpha1.BackfillViaRequested
			b.Spec.Repositories = []string{fmt.Sprintf("other/r%d", i)}
		}))
	}
	s := repoServer(t, authz.Grants{View: true}, &fakeAccess{readable: map[string]string{"acme/new": "gh"}}, objs...)
	code, body := doRequest(t, s, http.MethodPost, "/api/repositories/backfill",
		`{"repository":"acme/new","request":true}`)
	if code != http.StatusTooManyRequests || !strings.Contains(body, "waiting") {
		t.Fatalf("status = %d (%s), want 429 at the open-request cap", code, body)
	}
}

func countRequests(t *testing.T, s *Server) int {
	t.Helper()
	var list v1alpha1.BackfillRequestList
	if err := mustClient(s).List(t.Context(), &list); err != nil {
		t.Fatal(err)
	}
	return len(list.Items)
}

func TestBackfillDecision(t *testing.T) {
	requested := func(name string, mutate ...func(*v1alpha1.BackfillRequest)) *v1alpha1.BackfillRequest {
		return testBackfillRequest(name, -time.Minute, append([]func(*v1alpha1.BackfillRequest){
			func(b *v1alpha1.BackfillRequest) {
				b.Spec.Via = v1alpha1.BackfillViaRequested
				b.Spec.Repositories = []string{"acme/shop"}
			}}, mutate...)...)
	}
	decided := func(b *v1alpha1.BackfillRequest) {
		b.Spec.Decision = &v1alpha1.BackfillDecision{Verdict: v1alpha1.BackfillApproved, By: "ops",
			At: metav1.NewTime(testClock)}
	}
	cases := []struct {
		name, path, body string
		grants           authz.Grants
		obj              *v1alpha1.BackfillRequest
		want             int
		wantVerdict      v1alpha1.BackfillVerdict
	}{
		{"approve", "/api/backfill-requests/r/approve", "", allGrants(), requested("r"), http.StatusOK,
			v1alpha1.BackfillApproved},
		{"deny with a reason", "/api/backfill-requests/r/deny", `{"reason":"scanned last week"}`, allGrants(),
			requested("r"), http.StatusOK, v1alpha1.BackfillDeniedVerdict},
		{"no backfill grant", "/api/backfill-requests/r/approve", "", authz.Grants{View: true}, requested("r"),
			http.StatusForbidden, ""},
		{"already decided", "/api/backfill-requests/r/deny", "", allGrants(), requested("r", decided),
			http.StatusConflict, v1alpha1.BackfillApproved},
		{"needs no decision", "/api/backfill-requests/r/approve", "", allGrants(), testBackfillRequest("r", 0),
			http.StatusConflict, ""},
		{"unknown verdict", "/api/backfill-requests/r/maybe", "", allGrants(), requested("r"), http.StatusNotFound, ""},
		{"unknown request", "/api/backfill-requests/nope/approve", "", allGrants(), requested("r"), http.StatusNotFound, ""},
		{"reason too long", "/api/backfill-requests/r/deny", `{"reason":"` + strings.Repeat("x", 501) + `"}`, allGrants(),
			requested("r"), http.StatusBadRequest, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := repoServer(t, tc.grants, nil, tc.obj)
			code, body := doRequest(t, s, http.MethodPost, tc.path, tc.body)
			if code != tc.want {
				t.Fatalf("status = %d (%s), want %d", code, body, tc.want)
			}
			var got v1alpha1.BackfillRequest
			if err := mustClient(s).Get(t.Context(), types.NamespacedName{Namespace: "patchy", Name: "r"}, &got); err != nil {
				t.Fatal(err)
			}
			switch d := got.Spec.Decision; {
			case tc.wantVerdict == "" && d != nil:
				t.Errorf("decision = %+v, want none", d)
			case tc.wantVerdict != "" && (d == nil || d.Verdict != tc.wantVerdict):
				t.Errorf("decision = %+v, want %s", d, tc.wantVerdict)
			case tc.want == http.StatusOK && d.By != "op@acme.test":
				t.Errorf("decision by %q, want the caller", d.By)
			}
		})
	}
}

func TestBackfillRequestsList(t *testing.T) {
	s := repoServer(t, authz.Grants{View: true}, nil,
		testBackfillRequest("older", -time.Hour),
		testBackfillRequest("newer", -time.Minute, func(b *v1alpha1.BackfillRequest) {
			b.Spec.Via = v1alpha1.BackfillViaMaintained
			b.Spec.GitHub = &v1alpha1.BackfillGitHubIdentity{Login: "octocat", Role: "maintain"}
		}))
	code, body := doRequest(t, s, http.MethodGet, "/api/backfill-requests", "")
	if code != http.StatusOK {
		t.Fatalf("status = %d: %s", code, body)
	}
	var out BackfillRequestsPayload
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if out.CanDecide || len(out.Requests) != 2 || out.Requests[0].Name != "newer" ||
		out.Requests[0].GitHubLogin != "octocat" || out.Requests[0].Via != "maintained" || out.Requests[1].Via != "backfill" {
		t.Errorf("payload = %+v", out)
	}
	if code, _ := doRequest(t, repoServer(t, authz.Grants{Metrics: true}, nil), http.MethodGet,
		"/api/backfill-requests", ""); code != http.StatusForbidden {
		t.Errorf("without view: status = %d, want 403", code)
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

func TestAgo(t *testing.T) {
	for d, want := range map[time.Duration]string{
		20 * time.Second:  "less than a minute",
		25 * time.Minute:  "25m",
		3 * time.Hour:     "3h",
		185 * time.Minute: "3h 5m",
	} {
		if got := ago(testClock.Add(-d), testClock); got != want {
			t.Errorf("ago(%v) = %q, want %q", d, got, want)
		}
	}
}
