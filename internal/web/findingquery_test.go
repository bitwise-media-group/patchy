// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/loadgen"
)

// qFinding is a list-shaped finding: what the query reads and nothing else.
type qFinding struct {
	name     string
	severity v1alpha1.Level
	phase    v1alpha1.Phase
	ago      time.Duration // first observed this long before testClock
	repo     string
	title    string
	verdict  v1alpha1.Recommendation
	owners   []string
	running  bool
	suspend  bool
}

func (q qFinding) build() *v1alpha1.Finding {
	at := metav1.NewTime(testClock.Add(-q.ago))
	f := &v1alpha1.Finding{
		ObjectMeta: metav1.ObjectMeta{Name: q.name, Namespace: "patchy", CreationTimestamp: at},
		Spec: v1alpha1.FindingSpec{
			IntegrationRef: v1alpha1.LocalObjectReference{Name: "gh"},
			Title:          q.title,
			Severity:       q.severity,
			Suspend:        q.suspend,
			Advisories:     []string{"CVE-2026-" + q.name},
		},
		Status: v1alpha1.FindingStatus{Phase: q.phase, FirstObservedAt: &at, Owners: q.owners},
	}
	if q.repo != "" {
		f.Spec.Repository = &v1alpha1.FindingRepository{Type: v1alpha1.RepositoryTypeGitHub, Name: q.repo}
	}
	if q.verdict != "" {
		f.Status.Investigation = &v1alpha1.InvestigationSummary{Recommendation: q.verdict}
	}
	if q.running {
		f.Status.ActiveRun = &v1alpha1.ActiveRun{Kind: v1alpha1.RunKindInvestigation, Name: q.name + "-inv"}
	}
	return f
}

func qServer(t *testing.T, fs ...qFinding) *Server {
	t.Helper()
	objs := make([]client.Object, len(fs))
	for i, f := range fs {
		objs[i] = f.build()
	}
	return testServer(t, objs...)
}

func names(p *FindingsPage) []string {
	out := make([]string, len(p.Findings))
	for i, f := range p.Findings {
		out[i] = f.Name
	}
	return out
}

func mustQuery(t *testing.T, raw string) findingQuery {
	t.Helper()
	v, err := url.ParseQuery(raw)
	if err != nil {
		t.Fatal(err)
	}
	fq, err := parseFindingQuery(v, defaultFindingLimit, maxFindingLimit)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return fq
}

func TestParseFindingQuery(t *testing.T) {
	good := encodeCursor(findingKey{severity: 1, phase: 2, observed: 3, name: "x"})
	for _, tc := range []struct {
		raw     string
		wantErr bool
	}{
		{"", false},
		{"phase=Opened&phase=Failed&severity=high&verdict=manual&repo=acme/orders&q=sql", false},
		{"limit=0", false},
		{"limit=100000", false}, // clamped, not rejected
		{"cursor=" + good, false},
		{"phase=opened", true}, // values are case-sensitive, as the CRD's are
		{"severity=urgent", true},
		{"verdict=maybe", true},
		{"limit=-1", true},
		{"limit=ten", true},
		{"cursor=!!!", true},
		{"cursor=" + encodeCursor(findingKey{name: "x"})[:4], true},
		{"q=" + string(make([]byte, maxFindingQuery+1)), true},
		{"repo=a/b&repo=a/c&repoPrefix=ord&repoContains=api", false},
		{"repoPrefix=", true}, // an empty term would match everything
		{"repoContains=%20", true},
		{"repo=" + strings.Repeat("x", maxFindingQuery+1), true},
		{strings.Repeat("repoPrefix=x&", maxRepoTerms+1), true},
	} {
		v, err := url.ParseQuery(tc.raw)
		if err != nil {
			t.Fatalf("ParseQuery(%.40q): %v", tc.raw, err)
		}
		fq, err := parseFindingQuery(v, defaultFindingLimit, maxFindingLimit)
		if (err != nil) != tc.wantErr {
			t.Errorf("%.40q: err = %v, wantErr %v", tc.raw, err, tc.wantErr)
		}
		if tc.raw == "limit=100000" && fq.limit != maxFindingLimit {
			t.Errorf("limit = %d, want clamped to %d", fq.limit, maxFindingLimit)
		}
	}
}

func TestCursorRoundTrip(t *testing.T) {
	// A Kubernetes name may contain dots; the name is the cursor's tail.
	k := findingKey{severity: 9, phase: 30, observed: -5, name: "finding.with.dots"}
	got, err := decodeCursor(encodeCursor(k))
	if err != nil || got != k {
		t.Fatalf("round trip = %+v, %v; want %+v", got, err, k)
	}
}

// The list order is severity, then phase (rail order, closed phases after),
// then newest first, then name.
func TestQueryFindingsOrder(t *testing.T) {
	s := qServer(t,
		qFinding{name: "low-new", severity: v1alpha1.LevelLow, phase: v1alpha1.PhaseOpened, ago: time.Minute},
		qFinding{name: "crit-failed", severity: v1alpha1.LevelCritical, phase: v1alpha1.PhaseFailed, ago: time.Hour},
		qFinding{name: "crit-queued-old", severity: v1alpha1.LevelCritical, phase: v1alpha1.PhaseQueued, ago: 2 * time.Hour},
		qFinding{name: "crit-queued-new", severity: v1alpha1.LevelCritical, phase: v1alpha1.PhaseQueued, ago: time.Hour},
		qFinding{name: "crit-opened", severity: v1alpha1.LevelCritical, phase: v1alpha1.PhaseOpened, ago: 5 * time.Hour},
		qFinding{name: "b-tie", severity: v1alpha1.LevelHigh, phase: v1alpha1.PhaseOpened, ago: time.Hour},
		qFinding{name: "a-tie", severity: v1alpha1.LevelHigh, phase: v1alpha1.PhaseOpened, ago: time.Hour},
		qFinding{name: "unrated", phase: v1alpha1.PhaseOpened},
	)
	p, err := s.queryFindings(t.Context(), mustQuery(t, ""), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"crit-opened", "crit-queued-new", "crit-queued-old", "crit-failed",
		"a-tie", "b-tie", "low-new", "unrated",
	}
	if got := names(p); !slices.Equal(got, want) {
		t.Errorf("order = %v\nwant    %v", got, want)
	}
	if p.Matched != len(want) || p.NextCursor != "" {
		t.Errorf("matched = %d, nextCursor = %q; want %d and none", p.Matched, p.NextCursor, len(want))
	}
}

// Paging by cursor walks the whole list exactly once, and a finding
// arriving mid-walk ahead of the cursor neither repeats nor drops a row.
func TestQueryFindingsKeysetPaging(t *testing.T) {
	fs := make([]qFinding, 0, 23)
	for i := range 23 {
		fs = append(fs, qFinding{
			name:     fmt.Sprintf("f-%02d", i),
			severity: []v1alpha1.Level{v1alpha1.LevelHigh, v1alpha1.LevelLow}[i%2],
			phase:    v1alpha1.PhaseOpened,
			ago:      time.Duration(i) * time.Minute,
		})
	}
	s := qServer(t, fs...)
	all, err := s.queryFindings(t.Context(), mustQuery(t, "limit=200"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	var walked []string
	cursor := ""
	for page := 0; ; page++ {
		raw := "limit=5"
		if cursor != "" {
			raw += "&cursor=" + cursor
		}
		p, err := s.queryFindings(t.Context(), mustQuery(t, raw), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if (page == 0) != (p.Facets != nil) {
			t.Errorf("page %d facets = %v; want them on the first page only", page, p.Facets != nil)
		}
		// Matched is live: it counts the finding created after page 1.
		if want := map[bool]int{true: 23, false: 24}[page <= 1]; p.Matched != want {
			t.Errorf("page %d matched = %d, want %d", page, p.Matched, want)
		}
		walked = append(walked, names(p)...)
		if page == 1 {
			// A new, most-severe finding lands ahead of the cursor.
			if err := s.client.Create(t.Context(), qFinding{
				name: "f-new", severity: v1alpha1.LevelCritical, phase: v1alpha1.PhaseOpened,
			}.build()); err != nil {
				t.Fatal(err)
			}
		}
		if p.NextCursor == "" {
			break
		}
		cursor = p.NextCursor
	}
	if want := names(all); !slices.Equal(walked, want) {
		t.Errorf("walked = %v\nwant     %v", walked, want)
	}
}

func TestQueryFindingsFilters(t *testing.T) {
	s := qServer(t,
		qFinding{name: "one", severity: v1alpha1.LevelHigh, phase: v1alpha1.PhaseOpened, repo: "acme/orders",
			title: "SQL Injection in Checkout", verdict: v1alpha1.RecommendationRemediate},
		qFinding{name: "two", severity: v1alpha1.LevelLow, phase: v1alpha1.PhaseFailed, repo: "acme/billing",
			owners: []string{"Team-Payments"}},
		qFinding{name: "three", severity: v1alpha1.LevelHigh, phase: v1alpha1.PhaseQueued, repo: "acme/orders",
			verdict: v1alpha1.RecommendationManual},
		qFinding{name: "four"}, // no severity, phase, repository or verdict yet
	)
	for _, tc := range []struct {
		raw  string
		want []string
	}{
		{"", []string{"one", "three", "two", "four"}},
		{"severity=high", []string{"one", "three"}},
		{"severity=high&severity=low", []string{"one", "three", "two"}},
		{"phase=Failed&phase=Queued", []string{"three", "two"}},
		{"verdict=remediate", []string{"one"}},
		{"repo=acme/orders", []string{"one", "three"}},
		{"repo=acme/orders&verdict=manual", []string{"three"}},
		// Repository terms are additive: any exact name, prefix or substring.
		{"repo=acme/orders&repo=acme/billing", []string{"one", "three", "two"}},
		{"repoPrefix=ORD", []string{"one", "three"}},
		{"repoPrefix=acme/b", []string{"two"}}, // a "/" matches the whole name
		{"repoPrefix=acme", []string{}},        // otherwise the owner is not the name
		{"repoContains=ill", []string{"two"}},
		{"repoContains=me", []string{}},
		{"repoPrefix=ord&repoContains=ill", []string{"one", "three", "two"}},
		{"repoPrefix=ord&severity=low", []string{}},
		// Text search folds case over the title, owners, advisories and name.
		{"q=injection", []string{"one"}},
		{"q=TEAM-pay", []string{"two"}},
		{"q=cve-2026-thr", []string{"three"}},
		{"q=billing", []string{"two"}},
		{"q=nothing-matches", []string{}},
	} {
		p, err := s.queryFindings(t.Context(), mustQuery(t, tc.raw), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := names(p); !slices.Equal(got, tc.want) {
			t.Errorf("%q = %v, want %v", tc.raw, got, tc.want)
		}
		if p.Matched != len(tc.want) {
			t.Errorf("%q matched = %d, want %d", tc.raw, p.Matched, len(tc.want))
		}
		// Facets ignore the filters: the rail always counts everything.
		if p.Facets == nil || p.Facets.Total != 4 {
			t.Errorf("%q facets = %+v, want total 4", tc.raw, p.Facets)
		}
	}
}

func TestQueryFindingsLimitZeroCountsOnly(t *testing.T) {
	s := qServer(t,
		qFinding{name: "a", severity: v1alpha1.LevelHigh},
		qFinding{name: "b", severity: v1alpha1.LevelHigh},
	)
	p, err := s.queryFindings(t.Context(), mustQuery(t, "limit=0&severity=high"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Findings) != 0 || p.Matched != 2 || p.NextCursor != "" {
		t.Errorf("page = %d rows, matched %d, cursor %q; want 0, 2, none", len(p.Findings), p.Matched, p.NextCursor)
	}
}

func TestQueryFindingsFacets(t *testing.T) {
	s := qServer(t,
		qFinding{name: "a", severity: v1alpha1.LevelHigh, phase: v1alpha1.PhaseOpened, repo: "acme/orders", running: true},
		qFinding{name: "b", severity: v1alpha1.LevelHigh, phase: v1alpha1.PhaseAwaitingApproval, repo: "acme/orders",
			verdict: v1alpha1.RecommendationRemediate, suspend: true},
		qFinding{name: "c", severity: v1alpha1.LevelLow, phase: v1alpha1.PhaseRemediated, repo: "acme/billing",
			verdict: v1alpha1.RecommendationRemediate},
		qFinding{name: "d", phase: v1alpha1.PhaseFailed},
		qFinding{name: "e"},
	)
	p, err := s.queryFindings(t.Context(), mustQuery(t, "phase=Failed"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := p.Facets
	want := &FindingFacets{
		Total: 5, Open: 2, Running: 1, Suspended: 1,
		Phases:     map[string]int{"Opened": 1, "AwaitingApproval": 1, "Remediated": 1, "Failed": 1},
		Severities: map[string]int{"high": 2, "low": 1},
		Verdicts:   map[string]int{"remediate": 2},
		Repositories: []RepositoryCount{
			{Name: "acme/orders", Findings: 2}, {Name: "acme/billing", Findings: 1},
		},
		RepositoryCount: 2,
	}
	gj, _ := json.Marshal(got)
	wj, _ := json.Marshal(want)
	if string(gj) != string(wj) {
		t.Errorf("facets = %s\nwant     %s", gj, wj)
	}
}

func TestSuggestFindings(t *testing.T) {
	var fs []qFinding
	add := func(repo string, n int, sev v1alpha1.Level) {
		for i := range n {
			name := fmt.Sprintf("f-%s-%d", strings.ReplaceAll(repo, "/", "-"), i)
			fs = append(fs, qFinding{name: name, repo: repo, severity: sev})
		}
	}
	add("acme/orders", 3, v1alpha1.LevelHigh)
	add("acme/order-api", 1, v1alpha1.LevelLow)
	add("acme/reorder", 5, v1alpha1.LevelLow)
	add("orders/web", 2, v1alpha1.LevelHigh)
	add("acme/billing", 4, v1alpha1.LevelHigh)
	s := qServer(t, fs...)

	v, _ := url.ParseQuery("q=order&severity=high")
	fq, err := parseFindingQuery(v, facetRepositories, maxSuggestLimit)
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.suggestFindings(t.Context(), fq)
	if err != nil {
		t.Fatal(err)
	}
	// The hit count applies the filters (high only) with q as the search.
	if out.Matched != 5 {
		t.Errorf("matched = %d, want 5 (acme/orders ×3, orders/web ×2)", out.Matched)
	}
	// Repositories ignore the filters and rank by match: name prefix (heaviest
	// first among equals), mid-word, then owner-only.
	got := make([]string, 0, len(out.Repositories))
	for _, r := range out.Repositories {
		got = append(got, r.Name)
	}
	want := []string{"acme/orders", "acme/order-api", "acme/reorder", "orders/web"}
	if !slices.Equal(got, want) || out.RepositoryTotal != 4 {
		t.Errorf("repositories = %v (total %d), want %v (total 4)", got, out.RepositoryTotal, want)
	}
	// The repository terms' reach, by name (owner aside): orders and
	// order-api start with it; reorder contains it too. orders/web's name
	// is "web".
	if want := (RepositoryMatch{Repositories: 2, Findings: 4}); out.Prefix != want {
		t.Errorf("prefix = %+v, want %+v", out.Prefix, want)
	}
	if want := (RepositoryMatch{Repositories: 3, Findings: 9}); out.Contains != want {
		t.Errorf("contains = %+v, want %+v", out.Contains, want)
	}
}

func TestFindingsRoutes(t *testing.T) {
	s := signedIn(qServer(t,
		qFinding{name: "finding-a-1", severity: v1alpha1.LevelHigh, repo: "acme/orders"},
		qFinding{name: "finding-b-1", severity: v1alpha1.LevelLow, repo: "acme/orders"},
	), allGrants())
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	get := func(path string, into any) int {
		t.Helper()
		res, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer func() { _ = res.Body.Close() }()
		if into != nil && res.StatusCode == http.StatusOK {
			if err := json.NewDecoder(res.Body).Decode(into); err != nil {
				t.Fatalf("decode %s: %v", path, err)
			}
		}
		return res.StatusCode
	}

	var first FindingsPage
	if code := get("/api/findings?limit=1", &first); code != http.StatusOK {
		t.Fatalf("first page status = %d", code)
	}
	if names(&first)[0] != "finding-a-1" || first.NextCursor == "" || first.Facets == nil {
		t.Fatalf("first page = %v, cursor %q, facets %v", names(&first), first.NextCursor, first.Facets)
	}
	var second FindingsPage
	if code := get("/api/findings?limit=1&cursor="+first.NextCursor, &second); code != http.StatusOK {
		t.Fatalf("second page status = %d", code)
	}
	if !slices.Equal(names(&second), []string{"finding-b-1"}) || second.NextCursor != "" || second.Facets != nil {
		t.Errorf("second page = %v, cursor %q, facets %v", names(&second), second.NextCursor, second.Facets)
	}

	// The suggest route is a literal, not a finding named "suggest".
	var sug FindingSuggestions
	if code := get("/api/findings/suggest?q=acme", &sug); code != http.StatusOK {
		t.Fatalf("suggest status = %d", code)
	}
	if sug.Matched != 2 || len(sug.Repositories) != 1 {
		t.Errorf("suggest = %+v", sug)
	}

	for _, bad := range []string{
		"/api/findings?phase=bogus",
		"/api/findings?cursor=bogus",
		"/api/findings/suggest?cursor=" + first.NextCursor,
	} {
		if code := get(bad, nil); code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400", bad, code)
		}
	}
}

func TestContainsFold(t *testing.T) {
	for _, tc := range []struct {
		s, needle string
		want      bool
	}{
		{"SQL Injection", "injection", true},
		{"SQL Injection", "sql i", true},
		{"abc", "", true},
		{"abc", "abcd", false},
		{"Ünïcode Title", "ïcode", true},
		{"ascii", "ï", false},
	} {
		if got := containsFold(tc.s, tc.needle); got != tc.want {
			t.Errorf("containsFold(%q, %q) = %v, want %v", tc.s, tc.needle, got, tc.want)
		}
	}
}

// cachedFindings stands in for the informer cache's UnsafeDisableDeepCopy
// List for findings. Like the real one, it skips the deep copy but still
// copies every Finding struct into the list it returns (the real cache
// does it through reflection in apimeta.SetList, so it is slower still).
type cachedFindings struct {
	client.Client
	items []v1alpha1.Finding
}

func (c cachedFindings) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if fl, ok := list.(*v1alpha1.FindingList); ok {
		fl.Items = slices.Clone(c.items)
		return nil
	}
	return c.Client.List(ctx, list, opts...)
}

// cachedFindingServer builds a server over n synthetic findings in its
// steady state: the mirror filled and synced, as once StartWatch has run.
// listedFindingServer leaves the mirror empty, so requests list the cache
// (the path before the mirror syncs).
func cachedFindingServer(n int) *Server {
	s := listedFindingServer(n)
	s.findings.fill(s.client.(cachedFindings).items)
	return s
}

func listedFindingServer(n int) *Server {
	items := loadgen.Findings(n, loadgen.Opts{PhaseMix: benchPhaseMix, AlertsPerFinding: 2})
	return NewServer(cachedFindings{items: items}, loadgen.Namespace, nil, nil, nil)
}

// fill mirrors items and marks the mirror synced.
func (st *findingStore) fill(items []v1alpha1.Finding) {
	for i := range items {
		st.upsert(&items[i])
	}
	synced := func() bool { return true }
	st.synced.Store(&synced)
}
