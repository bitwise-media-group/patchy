// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/loadgen"
	"github.com/bitwise-media-group/patchy/internal/web/authz"
)

// repoRollup is a repository shard with the given counters. remediated is
// part of findings; spend is the investigation stage's cost.
func repoRollup(key string, findings, remediated, failed, spend int64) *v1alpha1.FindingRollup {
	r := testRollup(v1alpha1.ScopeRepository, key, "repo-"+fmt.Sprint(len(key))+"-"+sanitize(key))
	r.Status.Monthly = nil
	r.Status.Bucket.Findings = findings
	r.Status.Bucket.Phases = map[string]int64{"remediated": remediated, "failed": failed}
	r.Status.Bucket.Stages = map[string]v1alpha1.StageAggregate{
		"investigation": {
			Runs: findings, Succeeded: findings, CostMicroUSD: spend,
			Outcomes: map[string]int64{"ok": findings},
		},
	}
	return r
}

func sanitize(s string) string {
	out := []byte(s)
	for i, c := range out {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			out[i] = '-'
		}
	}
	return string(out)
}

func keys(rs []Rollup) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Scope.Key
	}
	return out
}

func repoFixture(t *testing.T) *Server {
	t.Helper()
	return testServer(t,
		testRollup(v1alpha1.ScopeTotal, "", "total"),
		testRollup(v1alpha1.ScopeModel, "claude-opus-5", "model-opus"),
		repoRollup("acme/payments-api", 40, 30, 2, 40_000_000),
		repoRollup("acme/payments-worker", 12, 3, 4, 30_000_000),
		repoRollup("acme/ledger", 25, 20, 0, 10_000_000),
		repoRollup("acme/api-gateway", 5, 0, 1, 0),
		repoRollup("payments-org/legacy", 8, 4, 0, 4_000_000),
	)
}

func query(t *testing.T, s *Server, raw string) *RepositoryPage {
	t.Helper()
	v, err := url.ParseQuery(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	rq, err := parseRepoQuery(v)
	if err != nil {
		t.Fatalf("parseRepoQuery(%q): %v", raw, err)
	}
	page, err := s.queryRepositories(t.Context(), rq)
	if err != nil {
		t.Fatalf("queryRepositories(%q): %v", raw, err)
	}
	return page
}

func TestParseRepoQuery(t *testing.T) {
	tests := []struct {
		raw       string
		wantErr   bool
		sort      string
		desc      bool
		limit     int
		aggregate bool
	}{
		{raw: "", sort: sortFindings, desc: true, limit: defaultRepositoryLimit},
		{raw: "q=pay", sort: sortRelevance, desc: false, limit: defaultRepositoryLimit},
		{raw: "sort=name", sort: sortName, desc: false, limit: defaultRepositoryLimit},
		{raw: "sort=spend&order=asc&limit=500", sort: sortSpend, desc: false, limit: maxRepositoryLimit},
		{raw: "q=x&aggregate=1", sort: sortRelevance, limit: defaultRepositoryLimit, aggregate: true},
		{raw: "sort=bogus", wantErr: true},
		{raw: "sort=relevance", wantErr: true},
		{raw: "order=sideways", wantErr: true},
		{raw: "offset=-1", wantErr: true},
		{raw: "limit=ten", wantErr: true},
		{raw: "q=" + string(make([]byte, maxRepositoryQuery+1)), wantErr: true},
	}
	for _, tc := range tests {
		v, _ := url.ParseQuery(tc.raw)
		rq, err := parseRepoQuery(v)
		if tc.wantErr {
			if !errors.Is(err, errBadQuery) {
				t.Errorf("%q: err = %v, want errBadQuery", tc.raw, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", tc.raw, err)
			continue
		}
		if rq.sort != tc.sort || rq.descending != tc.desc || rq.limit != tc.limit || rq.aggregate != tc.aggregate {
			t.Errorf("%q = %+v", tc.raw, rq)
		}
	}
}

func TestLocate(t *testing.T) {
	tests := []struct {
		key, needle string
		score       int
	}{
		{"acme/ledger", "ledger", 0},
		{"acme/ledger", "led", 1},
		{"acme/payments-api", "api", 2},
		{"acme/payments-api", "ment", 3},
		{"acme/ledger", "acme", 4},
		{"acme/ledger", "cme", 5},
		{"acme/ledger", "acme/le", 1}, // "/" in the needle: whole-key prefix
	}
	for _, tc := range tests {
		if got := locate(tc.key, tc.needle); got != tc.score {
			t.Errorf("locate(%q, %q) score = %d, want %d", tc.key, tc.needle, got, tc.score)
		}
	}
}

func TestQueryRepositoriesOrderAndPaging(t *testing.T) {
	s := repoFixture(t)

	page := query(t, s, "")
	if page.Total != 5 || page.Of != 5 {
		t.Fatalf("total/of = %d/%d, want 5/5", page.Total, page.Of)
	}
	want := []string{"acme/payments-api", "acme/ledger", "acme/payments-worker", "payments-org/legacy", "acme/api-gateway"}
	if got := keys(page.Repositories); !slices.Equal(got, want) {
		t.Errorf("by findings = %v, want %v", got, want)
	}

	page = query(t, s, "offset=3&limit=10")
	if page.Offset != 3 || !slices.Equal(keys(page.Repositories), want[3:]) {
		t.Errorf("page 2 = offset %d %v", page.Offset, keys(page.Repositories))
	}
	if page = query(t, s, "offset=99"); page.Offset != 5 || len(page.Repositories) != 0 {
		t.Errorf("past the end = offset %d, %d rows", page.Offset, len(page.Repositories))
	}

	got := keys(query(t, s, "sort=name").Repositories)
	if got[0] != "acme/api-gateway" || got[4] != "payments-org/legacy" {
		t.Errorf("by name = %v", got)
	}
	// No remediations means no cost per remediation: it sorts last, not first.
	got = keys(query(t, s, "sort=costPerRemediation").Repositories)
	if got[0] != "acme/payments-worker" || got[4] != "acme/api-gateway" {
		t.Errorf("by cost per remediation = %v", got)
	}
	if got := keys(query(t, s, "sort=failed&order=asc").Repositories); got[0] != "acme/ledger" {
		t.Errorf("by failed asc = %v", got)
	}
}

func TestQueryRepositoriesRelevance(t *testing.T) {
	s := repoFixture(t)
	page := query(t, s, "q=PAY")
	// Name prefixes first (heaviest first among them), then the owner match.
	want := []string{"acme/payments-api", "acme/payments-worker", "payments-org/legacy"}
	if got := keys(page.Repositories); !slices.Equal(got, want) || page.Total != 3 {
		t.Errorf("q=PAY = %v (total %d), want %v", got, page.Total, want)
	}
	if page := query(t, s, "q=nothing-like-it"); page.Total != 0 || len(page.Repositories) != 0 {
		t.Errorf("no match = %+v", page)
	}
}

func TestQueryRepositoriesKeyAndRank(t *testing.T) {
	s := repoFixture(t)
	page := query(t, s, "key=acme/ledger")
	if page.Total != 1 || page.Rank != 2 || keys(page.Repositories)[0] != "acme/ledger" {
		t.Errorf("key lookup = total %d rank %d %v", page.Total, page.Rank, keys(page.Repositories))
	}
	if page := query(t, s, "key=acme/missing"); page.Total != 0 || page.Rank != 0 {
		t.Errorf("missing key = %+v", page)
	}
}

func TestQueryRepositoriesAggregate(t *testing.T) {
	s := repoFixture(t)
	before := query(t, s, "key=acme/payments-api").Repositories[0].Bucket.Phases["remediated"]

	page := query(t, s, "q=payments&aggregate=1&limit=1")
	agg := page.Aggregate
	if agg == nil {
		t.Fatal("aggregate missing")
	}
	// Sums every match, not just the one-row page.
	if agg.Bucket.Findings != 60 || agg.Bucket.Phases["remediated"] != 37 || agg.Bucket.Phases["failed"] != 6 {
		t.Errorf("aggregate bucket = %+v", agg.Bucket)
	}
	inv := agg.Bucket.Stages["investigation"]
	if inv.CostMicroUSD != 74_000_000 || inv.Outcomes["ok"] != 60 {
		t.Errorf("aggregate stage = %+v", inv)
	}
	if len(page.Repositories) != 1 {
		t.Errorf("page rows = %d, want 1", len(page.Repositories))
	}
	// Aggregating must never write through to the shared cache objects.
	if after := query(t, s, "key=acme/payments-api").Repositories[0].Bucket.Phases["remediated"]; after != before {
		t.Errorf("source shard mutated: remediated %d → %d", before, after)
	}
}

func TestRollupSummaryTrimsRepositories(t *testing.T) {
	objs := make([]client.Object, 0, 2+summaryRepositories+5)
	objs = append(objs,
		testRollup(v1alpha1.ScopeTotal, "", "total"),
		testRollup(v1alpha1.ScopeHarness, "claude-code", "harness-cc"),
	)
	for i := range summaryRepositories + 5 {
		objs = append(objs, repoRollup(fmt.Sprintf("acme/r%02d", i), int64(i+1), 1, 0, 1))
	}
	s := testServer(t, objs...)
	ds, err := s.buildRollupSummary(t.Context(), nil)
	if err != nil {
		t.Fatalf("buildRollupSummary: %v", err)
	}
	if ds.RepositoryCount != summaryRepositories+5 {
		t.Errorf("repositoryCount = %d", ds.RepositoryCount)
	}
	var repos []string
	for _, r := range ds.Rollups {
		if r.Scope.Type == "repository" {
			repos = append(repos, r.Scope.Key)
		}
	}
	if len(repos) != summaryRepositories || repos[0] != "acme/r14" {
		t.Errorf("summary repositories = %v, want the top %d by findings", repos, summaryRepositories)
	}
	if ds.Rollups[0].Scope.Type != "harness" || ds.Rollups[1].Scope.Type != "total" {
		t.Errorf("fixed shards = %+v %+v", ds.Rollups[0].Scope, ds.Rollups[1].Scope)
	}
}

func TestRepositoriesRoute(t *testing.T) {
	s := signedIn(repoFixture(t), authz.Grants{Metrics: true})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	res, err := http.Get(ts.URL + "/api/rollups/repositories?q=ledger")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	var page RepositoryPage
	if err := json.NewDecoder(res.Body).Decode(&page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if page.Total != 1 || page.Repositories[0].Scope.Key != "acme/ledger" {
		t.Errorf("page = %+v", page)
	}

	bad, err := http.Get(ts.URL + "/api/rollups/repositories?sort=bogus")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Errorf("bad sort status = %d, want 400", bad.StatusCode)
	}
}

// cachedRollups stands in for the informer cache's UnsafeDisableDeepCopy
// List: it hands back the stored shards without copying them, which the
// fake client cannot do (it round-trips every object per List).
type cachedRollups struct {
	client.Client
	items []v1alpha1.FindingRollup
}

func (c cachedRollups) List(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
	list.(*v1alpha1.FindingRollupList).Items = c.items
	return nil
}

// BenchmarkQueryRepositories measures one request at the scale of the
// largest known single organisation (~11k repositories), over a reader that
// behaves like the real cache path (no per-request copies).
func BenchmarkQueryRepositories(b *testing.B) {
	const repos = 11_000
	s := NewServer(cachedRollups{items: loadgen.Rollups(repos + 1)}, loadgen.Namespace, nil, nil, nil)
	for _, tc := range []struct{ name, raw string }{
		{"typeahead", "q=repo-12&limit=8"},
		{"table-page", "sort=costPerRemediation&offset=500&limit=25"},
		{"aggregate", "q=repo-1&aggregate=1&limit=10"},
		{"key", "key=loadgen/repo-5000"},
	} {
		v, _ := url.ParseQuery(tc.raw)
		rq, err := parseRepoQuery(v)
		if err != nil {
			b.Fatal(err)
		}
		b.Run(tc.name, func(b *testing.B) {
			for b.Loop() {
				if _, err := s.queryRepositories(b.Context(), rq); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
