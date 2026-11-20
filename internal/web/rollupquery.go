// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package web

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
)

// Repository rollups are the one scope that grows with the estate — an
// organisation can carry tens of thousands — so they never ship whole. The
// summary (GET /api/rollups) carries the total, model and harness shards and
// the top repositories; everything else is answered here, a page at a time:
// the metrics filter's type-ahead, the sortable repository table, a single
// repository's slice, and the "every repository matching" aggregate.
//
// Every request is a linear pass over the cached shards with no deep copies
// (the objects are only read), which stays in low milliseconds at 10k+.

const (
	// summaryRepositories is how many repositories the summary carries — the
	// first page of the metrics table, by findings.
	summaryRepositories    = 10
	defaultRepositoryLimit = 10
	maxRepositoryLimit     = 100
	maxRepositoryQuery     = 200
)

// RepositoryPage is the payload behind GET /api/rollups/repositories. It
// mirrors ui/src/types.ts.
type RepositoryPage struct {
	// Total is how many repositories matched (all of them without q/key).
	Total int `json:"total"`
	// Of is how many repository shards exist at all.
	Of     int `json:"of"`
	Offset int `json:"offset"`
	// Repositories is the requested page, in the requested order.
	Repositories []Rollup `json:"repositories"`
	// Aggregate sums every match (not just the page) when aggregate=1.
	Aggregate *Rollup `json:"aggregate,omitempty"`
	// Rank is the 1-based position by findings closed, for a key= lookup.
	Rank int `json:"rank,omitempty"`
}

// Repository sort orders. Relevance is the type-ahead ranking and needs q.
const (
	sortRelevance          = "relevance"
	sortFindings           = "findings"
	sortRemediated         = "remediated"
	sortFailed             = "failed"
	sortSpend              = "spend"
	sortCostPerRemediation = "costPerRemediation"
	sortName               = "name"
)

var repoSorts = []string{
	sortRelevance, sortFindings, sortRemediated, sortFailed, sortSpend, sortCostPerRemediation, sortName,
}

type repoQuery struct {
	q, key     string
	sort       string
	descending bool
	offset     int
	limit      int
	aggregate  bool
}

var errBadQuery = errors.New("bad query")

func parseRepoQuery(v url.Values) (repoQuery, error) {
	rq := repoQuery{
		q:         strings.TrimSpace(v.Get("q")),
		key:       v.Get("key"),
		sort:      v.Get("sort"),
		limit:     defaultRepositoryLimit,
		aggregate: v.Get("aggregate") == "1" || v.Get("aggregate") == "true",
	}
	if len(rq.q) > maxRepositoryQuery || len(rq.key) > maxRepositoryQuery {
		return rq, fmt.Errorf("%w: q and key are limited to %d bytes", errBadQuery, maxRepositoryQuery)
	}
	if rq.sort == "" {
		rq.sort = sortFindings
		if rq.q != "" {
			rq.sort = sortRelevance
		}
	}
	if !slices.Contains(repoSorts, rq.sort) {
		return rq, fmt.Errorf("%w: sort must be one of %s", errBadQuery, strings.Join(repoSorts, ", "))
	}
	if rq.sort == sortRelevance && rq.q == "" {
		return rq, fmt.Errorf("%w: sort=relevance needs q", errBadQuery)
	}
	// Numbers read biggest-first and names A–Z unless told otherwise.
	rq.descending = rq.sort != sortName && rq.sort != sortRelevance
	switch v.Get("order") {
	case "":
	case "asc":
		rq.descending = false
	case "desc":
		rq.descending = true
	default:
		return rq, fmt.Errorf("%w: order must be asc or desc", errBadQuery)
	}
	var err error
	if rq.offset, err = intParam(v, "offset", 0); err != nil {
		return rq, err
	}
	if rq.limit, err = intParam(v, "limit", defaultRepositoryLimit); err != nil {
		return rq, err
	}
	rq.limit = min(rq.limit, maxRepositoryLimit)
	return rq, nil
}

func intParam(v url.Values, name string, def int) (int, error) {
	raw := v.Get(name)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%w: %s must be a non-negative integer", errBadQuery, name)
	}
	return n, nil
}

// repoRow is one repository shard with the figures every order needs,
// computed once per request rather than per comparison.
type repoRow struct {
	fr    *v1alpha1.FindingRollup
	key   string
	lower string
	// Sort keys; -1 means "no figure" and sorts below every real value.
	findings, failed, spend int64
	remediatedShare         float64
	costPerRemediation      float64
	// Type-ahead ranking; lower is better.
	score int
}

func newRepoRow(fr *v1alpha1.FindingRollup) repoRow {
	b := &fr.Status.Bucket
	r := repoRow{
		fr:                 fr,
		key:                fr.Spec.Scope.Key,
		lower:              strings.ToLower(fr.Spec.Scope.Key),
		findings:           b.Findings,
		failed:             -1,
		remediatedShare:    -1,
		costPerRemediation: -1,
	}
	for _, st := range b.Stages {
		r.spend += st.CostMicroUSD
	}
	if b.Phases != nil {
		r.failed = b.Phases["failed"]
		if b.Findings > 0 {
			r.remediatedShare = float64(b.Phases["remediated"]) / float64(b.Findings)
		}
		if rem := b.Phases["remediated"]; rem > 0 && r.spend > 0 {
			r.costPerRemediation = float64(r.spend) / float64(rem)
		}
	}
	return r
}

// locate ranks a needle's match in a key (lower is better); see
// ui/src/typeahead.ts, which this mirrors so the type-ahead orders the same
// against the server and offline. For "owner/name" keys the name is searched
// first — in a single-organisation install every key shares the owner.
// Exact name beats name prefix beats word start beats mid-word; owner-only
// matches come last. A needle containing "/" matches the whole key.
func locate(key, needle string) (at, score int) {
	slash := -1
	if !strings.Contains(needle, "/") {
		slash = strings.LastIndex(key, "/")
	}
	name := key[slash+1:]
	if n := strings.Index(name, needle); n >= 0 {
		at = slash + 1 + n
		switch {
		case n == 0 && len(needle) == len(name):
			return at, 0
		case n == 0:
			return at, 1
		case strings.ContainsRune("/-_. ", rune(name[n-1])):
			return at, 2
		default:
			return at, 3
		}
	}
	at = strings.Index(key, needle)
	if at == 0 {
		return at, 4
	}
	return at, 5
}

func compareRows(a, b *repoRow, sort string, descending bool) int {
	dir := 1
	if descending {
		dir = -1
	}
	var c int
	switch sort {
	case sortRelevance:
		// Relevance has no direction: best first, heaviest first among equals.
		if c = cmp.Compare(a.score, b.score); c == 0 {
			c = cmp.Compare(b.findings, a.findings)
		}
		dir = 1
	case sortName:
		c = strings.Compare(a.key, b.key)
	case sortFindings:
		c = cmp.Compare(a.findings, b.findings)
	case sortRemediated:
		c = cmp.Compare(a.remediatedShare, b.remediatedShare)
	case sortFailed:
		c = cmp.Compare(a.failed, b.failed)
	case sortSpend:
		c = cmp.Compare(a.spend, b.spend)
	case sortCostPerRemediation:
		c = cmp.Compare(a.costPerRemediation, b.costPerRemediation)
	}
	if c != 0 {
		return dir * c
	}
	// Stable across refetches: findings, then name.
	if c = cmp.Compare(b.findings, a.findings); c != 0 {
		return c
	}
	return strings.Compare(a.key, b.key)
}

// listRollups reads every shard from the cache without deep copies. Callers
// must treat the objects as read-only — projectRollup only reads them, and
// sumRollups builds fresh maps.
func (s *Server) listRollups(ctx context.Context) ([]v1alpha1.FindingRollup, error) {
	var list v1alpha1.FindingRollupList
	if err := s.client.List(ctx, &list, client.InNamespace(s.namespace), client.UnsafeDisableDeepCopy); err != nil {
		return nil, fmt.Errorf("list rollups: %w", err)
	}
	return list.Items, nil
}

func (s *Server) queryRepositories(ctx context.Context, rq repoQuery) (*RepositoryPage, error) {
	items, err := s.listRollups(ctx)
	if err != nil {
		return nil, err
	}
	all := make([]repoRow, 0, len(items))
	for i := range items {
		if items[i].Spec.Scope.Type == v1alpha1.ScopeRepository {
			all = append(all, newRepoRow(&items[i]))
		}
	}
	page := &RepositoryPage{Of: len(all), Repositories: []Rollup{}}

	matches := all
	switch {
	case rq.key != "":
		matches = nil
		for i := range all {
			if all[i].key == rq.key {
				matches = append(matches, all[i])
				ahead := 0
				for j := range all {
					if all[j].findings > all[i].findings {
						ahead++
					}
				}
				page.Rank = ahead + 1
				break
			}
		}
	case rq.q != "":
		needle := strings.ToLower(rq.q)
		matches = make([]repoRow, 0, len(all)/4)
		for i := range all {
			if !strings.Contains(all[i].lower, needle) {
				continue
			}
			row := all[i]
			_, row.score = locate(row.lower, needle)
			matches = append(matches, row)
		}
	}
	page.Total = len(matches)

	if rq.aggregate && len(matches) > 0 {
		agg := sumRollups(matches)
		page.Aggregate = &agg
	}

	slices.SortFunc(matches, func(a, b repoRow) int { return compareRows(&a, &b, rq.sort, rq.descending) })
	page.Offset = min(rq.offset, len(matches))
	end := min(page.Offset+rq.limit, len(matches))
	for i := page.Offset; i < end; i++ {
		page.Repositories = append(page.Repositories, projectRollup(matches[i].fr))
	}
	return page, nil
}

// sumRollups adds repository shards into one repository-shaped rollup. Every
// counter is additive, so the sum is exactly what a shard over the union
// would carry. Fresh maps throughout: the inputs are shared cache objects.
func sumRollups(rows []repoRow) Rollup {
	var bucket v1alpha1.RollupBucket
	bucket.Phases = map[string]int64{}
	bucket.Recommendations = map[string]int64{}
	bucket.Stages = map[string]v1alpha1.StageAggregate{}
	var first, last *metav1.Time
	for i := range rows {
		st := &rows[i].fr.Status
		b := &st.Bucket
		bucket.Findings += b.Findings
		bucket.Attempts += b.Attempts
		for k, v := range b.Phases {
			bucket.Phases[k] += v
		}
		for k, v := range b.Recommendations {
			bucket.Recommendations[k] += v
		}
		for name, s := range b.Stages {
			bucket.Stages[name] = addStage(bucket.Stages[name], s)
		}
		if t := st.FirstProcessed; t != nil && (first == nil || t.Before(first)) {
			first = t
		}
		if t := st.LastProcessed; t != nil && (last == nil || last.Before(t)) {
			last = t
		}
	}
	sum := v1alpha1.FindingRollup{
		Spec:   v1alpha1.FindingRollupSpec{Scope: v1alpha1.RollupScope{Type: v1alpha1.ScopeRepository}},
		Status: v1alpha1.FindingRollupStatus{Bucket: bucket, FirstProcessed: first, LastProcessed: last},
	}
	return projectRollup(&sum)
}

func addStage(into, s v1alpha1.StageAggregate) v1alpha1.StageAggregate {
	into.Runs += s.Runs
	into.Succeeded += s.Succeeded
	into.InputTokens += s.InputTokens
	into.OutputTokens += s.OutputTokens
	into.CacheReadTokens += s.CacheReadTokens
	into.CacheCreationTokens += s.CacheCreationTokens
	into.CostMicroUSD += s.CostMicroUSD
	into.ElapsedMilliseconds += s.ElapsedMilliseconds
	into.Turns += s.Turns
	if len(s.Outcomes) > 0 {
		outcomes := make(map[string]int64, len(into.Outcomes)+len(s.Outcomes))
		for k, v := range into.Outcomes {
			outcomes[k] = v
		}
		for k, v := range s.Outcomes {
			outcomes[k] += v
		}
		into.Outcomes = outcomes
	}
	if e := s.Estimate; e != nil {
		sum := v1alpha1.EstimateAggregate{}
		if into.Estimate != nil {
			sum = *into.Estimate
		}
		sum.Runs += e.Runs
		sum.PredictedTurns += e.PredictedTurns
		sum.ActualTurns += e.ActualTurns
		sum.PredictedOutputTokens += e.PredictedOutputTokens
		sum.ActualOutputTokens += e.ActualOutputTokens
		into.Estimate = &sum
	}
	return into
}

// handleRepositories serves GET /api/rollups/repositories: public, like the
// rest of the statistics surface.
func (s *Server) handleRepositories(w http.ResponseWriter, r *http.Request) {
	rq, err := parseRepoQuery(r.URL.Query())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	page, err := s.queryRepositories(r.Context(), rq)
	if err != nil {
		s.log.LogAttrs(r.Context(), slog.LevelError, "query repository rollups", slog.Any("error", err))
		http.Error(w, "failed to load repository rollups", http.StatusInternalServerError)
		return
	}
	writeJSONGzip(w, r, page)
}
