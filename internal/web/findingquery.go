// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package web

import (
	"cmp"
	"container/heap"
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/version"
)

// Findings are the scope that grows fastest (an onboarding backlog runs to
// millions), so the list never ships whole. GET /api/findings answers one
// page of the filtered, sorted list at a time. Paging is by keyset (a cursor
// naming the last row's sort key), not offset: the list changes while
// someone scrolls it, and an offset would repeat or skip rows as findings
// ahead of it come and go. The first page (no cursor) also carries the
// facets over every finding: the phase rail, the filter's tallies, and the
// repositories with most findings. GET /api/findings/suggest answers the
// filter's type-ahead.
//
// Every request is one linear pass over the cached findings with no copies
// (findingstore.go; the objects are only read). The best `limit` rows past the cursor
// are kept in a bounded heap, so a page costs O(n log limit) at any depth.
// ui/src/filters.ts answers the same queries offline with the same
// semantics; keep the two in step.

const (
	defaultFindingLimit = 50
	maxFindingLimit     = 200
	maxFindingQuery     = 200
	// facetRepositories is how many repositories the facets carry: the
	// filter's "most findings" suggestions before anything is typed.
	facetRepositories = 6
	maxSuggestLimit   = 20
	// maxRepoTerms caps repo, repoPrefix and repoContains together.
	maxRepoTerms = 50
)

// FindingsPage is the payload behind GET /api/findings. It mirrors
// ui/src/types.ts.
type FindingsPage struct {
	GeneratedAt string `json:"generatedAt"`
	Namespace   string `json:"namespace,omitempty"`
	Version     string `json:"version,omitempty"`
	User        *User  `json:"user,omitempty"`
	// Findings is the requested page, most severe first.
	Findings []FindingSummary `json:"findings"`
	// Matched is how many findings match the filters, across every page.
	Matched int `json:"matched"`
	// NextCursor continues the list after this page; absent on the last.
	NextCursor string `json:"nextCursor,omitempty"`
	// Facets summarise every finding, filters aside. First page only.
	Facets *FindingFacets `json:"facets,omitempty"`
}

// FindingFacets counts every finding in the namespace, whatever the
// filters: what the phase rail and the filter's suggestions show.
type FindingFacets struct {
	Total     int `json:"total"`
	Open      int `json:"open"`
	Running   int `json:"running"`
	Suspended int `json:"suspended"`
	// Phases, Severities and Verdicts count findings by value; a finding
	// without one (no phase yet, no verdict yet) counts in none.
	Phases     map[string]int `json:"phases,omitempty"`
	Severities map[string]int `json:"severities,omitempty"`
	Verdicts   map[string]int `json:"verdicts,omitempty"`
	// Repositories are the ones with most findings, heaviest first.
	Repositories []RepositoryCount `json:"repositories,omitempty"`
	// RepositoryCount is how many distinct repositories have findings.
	RepositoryCount int `json:"repositoryCount"`
}

// RepositoryCount is one repository and how many findings name it.
type RepositoryCount struct {
	Name     string `json:"name"`
	Findings int    `json:"findings"`
}

// FindingSuggestions is the payload behind GET /api/findings/suggest: the
// filter's type-ahead for one typed term.
type FindingSuggestions struct {
	// Matched is how many findings the filters match with the term as the
	// text search: what picking "Search for …" would list.
	Matched int `json:"matched"`
	// Repositories are the best matches for the term by name, ranked as
	// ui/src/typeahead.ts ranks them; the heaviest when the term is empty.
	Repositories []RepositoryCount `json:"repositories"`
	// RepositoryTotal is how many repositories match the term in all.
	RepositoryTotal int `json:"repositoryTotal"`
	// Prefix and Contains size the "repositories starting with" and
	// "repositories containing" filters for the term (filters aside).
	Prefix   RepositoryMatch `json:"prefix"`
	Contains RepositoryMatch `json:"contains"`
}

// RepositoryMatch is how many repositories a repository-name term matches,
// and how many findings name them.
type RepositoryMatch struct {
	Repositories int `json:"repositories"`
	Findings     int `json:"findings"`
}

var (
	queryPhases = []string{
		string(v1alpha1.PhaseOpened), string(v1alpha1.PhaseEnhanced), string(v1alpha1.PhaseInvestigating),
		string(v1alpha1.PhaseAwaitingApproval), string(v1alpha1.PhaseQueued), string(v1alpha1.PhaseRemediating),
		string(v1alpha1.PhaseInReview), string(v1alpha1.PhaseRemediated), string(v1alpha1.PhaseFailed),
		string(v1alpha1.PhaseDismissed), string(v1alpha1.PhaseHandedOff),
	}
	querySeverities = []string{
		string(v1alpha1.LevelCritical), string(v1alpha1.LevelHigh),
		string(v1alpha1.LevelMedium), string(v1alpha1.LevelLow),
	}
	queryVerdicts = []string{
		string(v1alpha1.RecommendationRemediate), string(v1alpha1.RecommendationIgnore),
		string(v1alpha1.RecommendationManual),
	}
)

// findingQuery is a parsed GET /api/findings (or /suggest) query string.
type findingQuery struct {
	phases, severities, verdicts []string
	// The repository filter: exact names, and lower-cased name prefixes and
	// substrings. A finding passes when it matches any of them.
	repos, repoPrefixes, repoContains []string
	// needle is the text search, lower-cased.
	needle string
	limit  int
	cursor *findingKey
}

func parseFindingQuery(v url.Values, defLimit, maxLimit int) (findingQuery, error) {
	fq := findingQuery{
		repos:        v["repo"],
		repoPrefixes: lowerAll(v["repoPrefix"]),
		repoContains: lowerAll(v["repoContains"]),
		needle:       strings.ToLower(strings.TrimSpace(v.Get("q"))),
	}
	if len(fq.repos)+len(fq.repoPrefixes)+len(fq.repoContains) > maxRepoTerms {
		return fq, fmt.Errorf("%w: at most %d repo, repoPrefix and repoContains values", errBadQuery, maxRepoTerms)
	}
	if len(fq.needle) > maxFindingQuery {
		return fq, fmt.Errorf("%w: q is limited to %d bytes", errBadQuery, maxFindingQuery)
	}
	for _, terms := range [][]string{fq.repos, fq.repoPrefixes, fq.repoContains} {
		for _, t := range terms {
			if t == "" || len(t) > maxFindingQuery {
				return fq, fmt.Errorf("%w: repository values are 1 to %d bytes", errBadQuery, maxFindingQuery)
			}
		}
	}
	var err error
	if fq.phases, err = enumParam(v, "phase", queryPhases); err != nil {
		return fq, err
	}
	if fq.severities, err = enumParam(v, "severity", querySeverities); err != nil {
		return fq, err
	}
	if fq.verdicts, err = enumParam(v, "verdict", queryVerdicts); err != nil {
		return fq, err
	}
	if fq.limit, err = intParam(v, "limit", defLimit); err != nil {
		return fq, err
	}
	fq.limit = min(fq.limit, maxLimit)
	if raw := v.Get("cursor"); raw != "" {
		k, err := decodeCursor(raw)
		if err != nil {
			return fq, err
		}
		fq.cursor = &k
	}
	return fq, nil
}

func lowerAll(vals []string) []string {
	out := make([]string, 0, len(vals))
	for _, s := range vals {
		out = append(out, strings.ToLower(strings.TrimSpace(s)))
	}
	return out
}

// enumParam reads a repeatable parameter whose values must come from allowed.
func enumParam(v url.Values, name string, allowed []string) ([]string, error) {
	vals := v[name]
	for _, s := range vals {
		if !slices.Contains(allowed, s) {
			return nil, fmt.Errorf("%w: %s must be one of %s", errBadQuery, name, strings.Join(allowed, ", "))
		}
	}
	return vals, nil
}

// matches reports whether f passes every filter. An empty filter passes
// everything; a set filter fails a finding with no value for it.
func (fq *findingQuery) matches(f *v1alpha1.Finding) bool {
	if len(fq.phases) > 0 && !slices.Contains(fq.phases, string(f.Status.Phase)) {
		return false
	}
	if len(fq.severities) > 0 && !slices.Contains(fq.severities, string(f.Spec.Severity)) {
		return false
	}
	if len(fq.verdicts) > 0 && !slices.Contains(fq.verdicts, verdictOf(f)) {
		return false
	}
	if fq.filtersRepo() && !fq.matchesRepo(repoOf(f)) {
		return false
	}
	return fq.needle == "" || matchesSearch(f, fq.needle)
}

func (fq *findingQuery) filtersRepo() bool {
	return len(fq.repos)+len(fq.repoPrefixes)+len(fq.repoContains) > 0
}

// matchesRepo reports whether a repository name passes any repository term.
// A repo-less finding passes none.
func (fq *findingQuery) matchesRepo(repo string) bool {
	if repo == "" {
		return false
	}
	if slices.Contains(fq.repos, repo) {
		return true
	}
	for _, p := range fq.repoPrefixes {
		if repoTermMatch(repo, p, true) {
			return true
		}
	}
	for _, c := range fq.repoContains {
		if repoTermMatch(repo, c, false) {
			return true
		}
	}
	return false
}

// repoTermMatch matches a lower-cased term against the name part of an
// "owner/name" repository, or against the whole of it when the term holds
// a "/": in a single-organisation install every repository shares the
// owner, so "media" should not match them all. As locate does for ranking.
func repoTermMatch(repo, term string, prefix bool) bool {
	key := repo
	if !strings.Contains(term, "/") {
		key = repo[strings.LastIndexByte(repo, '/')+1:]
	}
	if prefix {
		return len(key) >= len(term) && containsFold(key[:len(term)], term)
	}
	return containsFold(key, term)
}

func verdictOf(f *v1alpha1.Finding) string {
	if inv := f.Status.Investigation; inv != nil {
		return string(inv.Recommendation)
	}
	return ""
}

func repoOf(f *v1alpha1.Finding) string {
	if r := f.Spec.Repository; r != nil {
		return r.Name
	}
	return ""
}

// matchesSearch is the text search: a case-insensitive substring of the
// name, title, rule, repository, any advisory, or any owner.
func matchesSearch(f *v1alpha1.Finding, needle string) bool {
	if containsFold(f.Name, needle) || containsFold(f.Spec.Title, needle) ||
		containsFold(f.Spec.RuleID, needle) || containsFold(repoOf(f), needle) {
		return true
	}
	for _, a := range f.Spec.Advisories {
		if containsFold(a, needle) {
			return true
		}
	}
	for _, o := range f.Status.Owners {
		if containsFold(o, needle) {
			return true
		}
	}
	return false
}

// containsFold reports whether s contains the lower-cased needle, ignoring
// case. ASCII text (nearly every field) is compared in place with no
// allocation, since this runs over every finding per request.
func containsFold(s, needle string) bool {
	if needle == "" {
		return true
	}
	for i := range len(s) {
		if s[i] >= utf8.RuneSelf {
			return strings.Contains(strings.ToLower(s), needle)
		}
	}
	n := len(needle)
	for i := 0; i+n <= len(s); i++ {
		j := 0
		for j < n && lowerASCII(s[i+j]) == needle[j] {
			j++
		}
		if j == n {
			return true
		}
	}
	return false
}

func lowerASCII(c byte) byte {
	if 'A' <= c && c <= 'Z' {
		return c + ('a' - 'A')
	}
	return c
}

// findingKey is one finding's position in the list order: most severe
// first, then active work in rail order before the closed phases, then the
// most recently observed, then by name so the order is total.
type findingKey struct {
	severity, phase int
	// observed is the first-observed time (creation time until set) in Unix
	// seconds, the precision of the projected RFC3339 stamps.
	observed int64
	name     string
}

var (
	severityRank = map[v1alpha1.Level]int{
		v1alpha1.LevelCritical: 0, v1alpha1.LevelHigh: 1, v1alpha1.LevelMedium: 2, v1alpha1.LevelLow: 3,
	}
	phaseRank = map[v1alpha1.Phase]int{
		v1alpha1.PhaseOpened: 0, v1alpha1.PhaseEnhanced: 1, v1alpha1.PhaseInvestigating: 2,
		v1alpha1.PhaseAwaitingApproval: 3, v1alpha1.PhaseQueued: 4, v1alpha1.PhaseRemediating: 5,
		v1alpha1.PhaseInReview: 6, v1alpha1.PhaseRemediated: 7,
		v1alpha1.PhaseFailed: 20, v1alpha1.PhaseDismissed: 21, v1alpha1.PhaseHandedOff: 22,
	}
)

const (
	unrankedSeverity = 9
	unrankedPhase    = 30
)

func keyOf(f *v1alpha1.Finding) findingKey {
	k := findingKey{severity: unrankedSeverity, phase: unrankedPhase, name: f.Name}
	if r, ok := severityRank[f.Spec.Severity]; ok {
		k.severity = r
	}
	if r, ok := phaseRank[f.Status.Phase]; ok {
		k.phase = r
	}
	k.observed = f.CreationTimestamp.Unix()
	if t := f.Status.FirstObservedAt; t != nil && !t.IsZero() {
		k.observed = t.Unix()
	}
	return k
}

func compareKeys(a, b *findingKey) int {
	if c := cmp.Compare(a.severity, b.severity); c != 0 {
		return c
	}
	if c := cmp.Compare(a.phase, b.phase); c != 0 {
		return c
	}
	if c := cmp.Compare(b.observed, a.observed); c != 0 {
		return c
	}
	return strings.Compare(a.name, b.name)
}

// The cursor is opaque to clients: base64url over "severity.phase.observed.name".
// The name is last because a Kubernetes name may itself contain dots.

func encodeCursor(k findingKey) string {
	raw := strconv.Itoa(k.severity) + "." + strconv.Itoa(k.phase) + "." +
		strconv.FormatInt(k.observed, 10) + "." + k.name
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(s string) (findingKey, error) {
	bad := fmt.Errorf("%w: cursor is not one this server issued", errBadQuery)
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return findingKey{}, bad
	}
	parts := strings.SplitN(string(raw), ".", 4)
	if len(parts) != 4 || parts[3] == "" {
		return findingKey{}, bad
	}
	var k findingKey
	if k.severity, err = strconv.Atoi(parts[0]); err != nil {
		return findingKey{}, bad
	}
	if k.phase, err = strconv.Atoi(parts[1]); err != nil {
		return findingKey{}, bad
	}
	if k.observed, err = strconv.ParseInt(parts[2], 10, 64); err != nil {
		return findingKey{}, bad
	}
	k.name = parts[3]
	return k, nil
}

// pageHeap keeps the best `limit` rows seen so far, worst on top, so each
// candidate costs one comparison unless it displaces the current worst.
type pageRow struct {
	index int
	key   findingKey
}

type pageHeap []pageRow

func (h pageHeap) Len() int           { return len(h) }
func (h pageHeap) Less(i, j int) bool { return compareKeys(&h[i].key, &h[j].key) > 0 }
func (h pageHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *pageHeap) Push(x any)        { *h = append(*h, x.(pageRow)) }
func (h *pageHeap) Pop() any {
	old := *h
	last := old[len(old)-1]
	*h = old[:len(old)-1]
	return last
}

func (h *pageHeap) offer(row pageRow, limit int) {
	if h.Len() < limit {
		heap.Push(h, row)
		return
	}
	if compareKeys(&row.key, &(*h)[0].key) < 0 {
		(*h)[0] = row
		heap.Fix(h, 0)
	}
}

// facetTally accumulates FindingFacets over one pass.
type facetTally struct {
	facets FindingFacets
	repos  map[string]int
}

func newFacetTally() *facetTally {
	return &facetTally{
		facets: FindingFacets{Phases: map[string]int{}, Severities: map[string]int{}, Verdicts: map[string]int{}},
		repos:  map[string]int{},
	}
}

func (t *facetTally) add(f *v1alpha1.Finding) {
	ft := &t.facets
	ft.Total++
	if p := f.Status.Phase; p != "" {
		ft.Phases[string(p)]++
		if !v1alpha1.Terminal(p) {
			ft.Open++
		}
	}
	if f.Status.ActiveRun != nil {
		ft.Running++
	}
	if f.Spec.Suspend {
		ft.Suspended++
	}
	if s := f.Spec.Severity; s != "" {
		ft.Severities[string(s)]++
	}
	if v := verdictOf(f); v != "" {
		ft.Verdicts[v]++
	}
	if r := repoOf(f); r != "" {
		t.repos[r]++
	}
}

func (t *facetTally) finish() *FindingFacets {
	t.facets.RepositoryCount = len(t.repos)
	t.facets.Repositories, _ = rankRepositories(t.repos, "", facetRepositories)
	return &t.facets
}

// rankRepositories returns the best `limit` repositories for a lower-cased
// needle and how many match in all. An empty needle ranks by findings; a
// term ranks by locate (as ui/src/typeahead.ts does), heaviest first among
// equals, then by name.
func rankRepositories(repos map[string]int, needle string, limit int) ([]RepositoryCount, int) {
	type scored struct {
		RepositoryCount
		score int
	}
	matches := make([]scored, 0, len(repos))
	for name, n := range repos {
		row := scored{RepositoryCount: RepositoryCount{Name: name, Findings: n}}
		if needle != "" {
			lower := strings.ToLower(name)
			if !strings.Contains(lower, needle) {
				continue
			}
			row.score = locate(lower, needle)
		}
		matches = append(matches, row)
	}
	slices.SortFunc(matches, func(a, b scored) int {
		if c := cmp.Compare(a.score, b.score); c != 0 {
			return c
		}
		if c := cmp.Compare(b.Findings, a.Findings); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	out := make([]RepositoryCount, 0, min(limit, len(matches)))
	for i := range min(limit, len(matches)) {
		out = append(out, matches[i].RepositoryCount)
	}
	return out, len(matches)
}

// queryFindings answers one page of GET /api/findings. userActions is
// stamped uniformly because RBAC grants are namespace-scoped; the client
// intersects them with each finding's state machine itself.
func (s *Server) queryFindings(
	ctx context.Context, fq findingQuery, verbs []string, user *User,
) (*FindingsPage, error) {
	items, err := s.listFindings(ctx)
	if err != nil {
		return nil, err
	}
	page := &FindingsPage{
		GeneratedAt: s.now().UTC().Format(time.RFC3339),
		Namespace:   s.namespace,
		Version:     version.Version,
		User:        user,
		Findings:    []FindingSummary{},
	}
	var facets *facetTally
	if fq.cursor == nil {
		facets = newFacetTally()
	}
	// One extra row says whether another page follows.
	keep := fq.limit + 1
	best := make(pageHeap, 0, min(keep, len(items)))
	for i := range items {
		f := items[i]
		if facets != nil {
			facets.add(f)
		}
		if !fq.matches(f) {
			continue
		}
		page.Matched++
		if fq.limit == 0 {
			continue
		}
		k := keyOf(f)
		if fq.cursor != nil && compareKeys(&k, fq.cursor) <= 0 {
			continue
		}
		best.offer(pageRow{index: i, key: k}, keep)
	}
	rows := []pageRow(best)
	slices.SortFunc(rows, func(a, b pageRow) int { return compareKeys(&a.key, &b.key) })
	if len(rows) > fq.limit {
		rows = rows[:fq.limit]
		page.NextCursor = encodeCursor(rows[len(rows)-1].key)
	}
	page.Findings = make([]FindingSummary, 0, len(rows))
	for _, r := range rows {
		page.Findings = append(page.Findings, projectFindingSummary(items[r.index], verbs))
	}
	if facets != nil {
		page.Facets = facets.finish()
	}
	return page, nil
}

// suggestFindings answers GET /api/findings/suggest: how many findings the
// filters match with q as the text search, the repositories (over every
// finding, filters aside) whose name contains q, and the reach of q as a
// repository prefix and substring term.
func (s *Server) suggestFindings(ctx context.Context, fq findingQuery) (*FindingSuggestions, error) {
	items, err := s.listFindings(ctx)
	if err != nil {
		return nil, err
	}
	out := &FindingSuggestions{}
	repos := map[string]int{}
	for i := range items {
		f := items[i]
		if fq.matches(f) {
			out.Matched++
		}
		if r := repoOf(f); r != "" {
			repos[r]++
		}
	}
	out.Repositories, out.RepositoryTotal = rankRepositories(repos, fq.needle, fq.limit)
	if fq.needle != "" {
		for name, n := range repos {
			if repoTermMatch(name, fq.needle, true) {
				out.Prefix.Repositories++
				out.Prefix.Findings += n
			}
			if repoTermMatch(name, fq.needle, false) {
				out.Contains.Repositories++
				out.Contains.Findings += n
			}
		}
	}
	return out, nil
}

// handleFindings serves one page of the findings list to an authenticated
// identity whose RBAC grants viewing; per-finding detail comes from
// /api/findings/{name}, and metrics from /api/rollups.
func (s *Server) handleFindings(w http.ResponseWriter, r *http.Request) {
	v, ok := s.authorize(w, r)
	if !ok {
		return
	}
	fq, err := parseFindingQuery(r.URL.Query(), defaultFindingLimit, maxFindingLimit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	page, err := s.queryFindings(r.Context(), fq, v.grants.Verbs, newUser(v))
	if err != nil {
		s.log.LogAttrs(r.Context(), slog.LevelError, "query findings", slog.Any("error", err))
		http.Error(w, "failed to load findings", http.StatusInternalServerError)
		return
	}
	writeJSONGzip(w, r, page)
}

// handleSuggest serves the findings filter's type-ahead behind the same
// view gate as the list. The route is a literal under /api/findings/, which
// a finding name cannot shadow: the integration controller names every
// finding "finding-<hash>-<generation>".
func (s *Server) handleSuggest(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorize(w, r); !ok {
		return
	}
	fq, err := parseFindingQuery(r.URL.Query(), facetRepositories, maxSuggestLimit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if fq.cursor != nil {
		http.Error(w, "bad query: suggest takes no cursor", http.StatusBadRequest)
		return
	}
	out, err := s.suggestFindings(r.Context(), fq)
	if err != nil {
		s.log.LogAttrs(r.Context(), slog.LevelError, "suggest findings", slog.Any("error", err))
		http.Error(w, "failed to load suggestions", http.StatusInternalServerError)
		return
	}
	writeJSONGzip(w, r, out)
}
