// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Tier-1 microbenchmarks for the status-server dataset path: projecting one
// Finding, querying a findings page at backlog scale, encoding it, and the
// SSE fan-out. Run via `mise run bench`;
// the TestComplexity* assertions are opt-in (PATCHY_BENCH_ASSERT=1). The
// 2M-findings query needs ~16GB and is also gated on
// PATCHY_BENCH_HUGE=1 (run it with -benchtime=1x).

package web

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/loadgen"
)

// benchPhaseMix is a realistic brownfield distribution: most findings
// terminal, a working set in flight.
var benchPhaseMix = map[v1alpha1.Phase]int{
	v1alpha1.PhaseOpened:        5,
	v1alpha1.PhaseEnhanced:      5,
	v1alpha1.PhaseInvestigating: 2,
	v1alpha1.PhaseQueued:        3,
	v1alpha1.PhaseRemediating:   1,
	v1alpha1.PhaseInReview:      4,
	v1alpha1.PhaseRemediated:    50,
	v1alpha1.PhaseDismissed:     15,
	v1alpha1.PhaseHandedOff:     10,
	v1alpha1.PhaseFailed:        5,
}

// maximalFinding is a Finding with every projection branch populated.
func maximalFinding() *v1alpha1.Finding {
	o := loadgen.Opts{AlertsPerFinding: 64, PhaseMix: map[v1alpha1.Phase]int{v1alpha1.PhaseRemediated: 1}}
	f := loadgen.Finding(0, o)
	now := metav1.NewTime(time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC))
	f.Spec.Related = []v1alpha1.RelatedFinding{
		{From: f.Name, To: "finding-other-1", Relationship: v1alpha1.RelationshipSuccessorOf},
	}
	f.Spec.Approval = &v1alpha1.Approval{By: "octocat", At: now, Note: "ship it"}
	f.Spec.Retry = &v1alpha1.ActionRequest{By: "octocat", At: now}
	f.Spec.Expedite = &v1alpha1.ActionRequest{By: "octocat", At: now}
	f.Status.PhaseTimes = []v1alpha1.PhaseTime{
		{Phase: v1alpha1.PhaseOpened, At: now}, {Phase: v1alpha1.PhaseEnhanced, At: now},
		{Phase: v1alpha1.PhaseInvestigating, At: now}, {Phase: v1alpha1.PhaseQueued, At: now},
		{Phase: v1alpha1.PhaseRemediating, At: now}, {Phase: v1alpha1.PhaseInReview, At: now},
		{Phase: v1alpha1.PhaseRemediated, At: now},
	}
	f.Status.Tracking = &v1alpha1.TrackingStatus{
		Integration: "github", IssueNumber: 42,
		URL: "https://github.com/loadgen/repo-0/issues/42", State: "open",
	}
	f.Status.Priority = v1alpha1.LevelHigh
	f.Status.Investigation = &v1alpha1.InvestigationSummary{
		Name: f.Name + "-inv-1", Attempt: 1, Outcome: "ok",
		Recommendation: v1alpha1.RecommendationRemediate, Confidence: "0.9",
		Exploitability: v1alpha1.RatingHigh, Likelihood: v1alpha1.RatingMedium,
		Impact: v1alpha1.RatingHigh, CompletedAt: &now,
		Estimate: &v1alpha1.AgentEstimate{MaxTurns: 40, TokenBudget: 200000},
	}
	f.Status.Remediation = &v1alpha1.RemediationSummary{
		Name: f.Name + "-rem-1", Attempt: 1, Outcome: "ok", Success: true,
		Branch: "patchy/" + f.Name, CompletedAt: &now,
	}
	f.Status.PullRequest = &v1alpha1.PullRequestStatus{
		Number: 7, URL: "https://github.com/loadgen/repo-0/pull/7", State: "merged", MergedAt: &now,
	}
	f.Status.Attempts = v1alpha1.AttemptCounts{Investigation: 1, Remediation: 1}
	f.Status.ActiveRun = &v1alpha1.ActiveRun{Kind: v1alpha1.RunKindRemediation, Name: f.Name + "-rem-1"}
	return f
}

// BenchmarkProjectFinding measures flattening one CR onto the wire type:
// the per-item cost inside every dataset build.
func BenchmarkProjectFinding(b *testing.B) {
	verbs := []string{"approve", "retry", "expedite", "suspend", "resume"}
	b.Run("shape=maximal", func(b *testing.B) {
		f := maximalFinding()
		b.ReportAllocs()
		for b.Loop() {
			_ = projectFinding(f, verbs)
		}
	})
	b.Run("shape=lean", func(b *testing.B) {
		f := loadgen.Finding(0, loadgen.Opts{})
		b.ReportAllocs()
		for b.Loop() {
			_ = projectFinding(f, verbs)
		}
	})
	// The summary projection is the per-item cost inside every list build.
	b.Run("shape=summary", func(b *testing.B) {
		f := maximalFinding()
		b.ReportAllocs()
		for b.Loop() {
			_ = projectFindingSummary(f, verbs)
		}
	})
}

// findingBenchQueries are the list requests a reader makes: the first page
// (which also tallies the facets), a page deep into the list, a filtered
// page, a text search, and the filter's type-ahead.
var findingBenchQueries = []struct{ name, raw string }{
	{"first-page", "limit=50"},
	{"deep-page", "limit=50&cursor=" + encodeCursor(findingKey{severity: 2, phase: 7, name: "m"})},
	{"filtered", "limit=50&phase=Queued&severity=high"},
	{"search", "limit=50&q=finding-12"},
}

// BenchmarkQueryFindings measures one GET /api/findings page at each
// backlog size, over a reader that behaves like the real cache path (no
// per-request copies): one pass with the filters, the bounded page heap,
// and the facets on the first page.
func BenchmarkQueryFindings(b *testing.B) {
	for _, n := range []int{10_000, 100_000} {
		s := cachedFindingServer(n)
		for _, q := range findingBenchQueries {
			b.Run(fmt.Sprintf("findings=%d/%s", n, q.name), func(b *testing.B) {
				benchQueryFindings(b, s, q.raw)
			})
		}
		b.Run(fmt.Sprintf("findings=%d/suggest", n), func(b *testing.B) {
			v, _ := url.ParseQuery("q=repo-1&severity=high")
			fq, err := parseFindingQuery(v, facetRepositories, maxSuggestLimit)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				if _, err := s.suggestFindings(b.Context(), fq); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkQueryFindingsHuge is the full 2M design target. ~16GB of working
// set: opt in with PATCHY_BENCH_HUGE=1 and run with -benchtime=1x.
// BenchmarkListFindings is the read that starts every findings request:
// a no-deep-copy cache List (the path until the mirror syncs), which still
// copies every Finding struct, against the mirror's pointer snapshot.
func BenchmarkListFindings(b *testing.B) {
	for _, n := range []int{10_000, 100_000} {
		for _, via := range []struct {
			name string
			s    *Server
		}{{"cache-list", listedFindingServer(n)}, {"mirror", cachedFindingServer(n)}} {
			b.Run(fmt.Sprintf("findings=%d/via=%s", n, via.name), func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					items, err := via.s.listFindings(b.Context())
					if err != nil || len(items) != n {
						b.Fatalf("listed %d, %v", len(items), err)
					}
				}
			})
		}
	}
}

func BenchmarkQueryFindingsHuge(b *testing.B) {
	if os.Getenv("PATCHY_BENCH_HUGE") == "" {
		b.Skip("set PATCHY_BENCH_HUGE=1 (and -benchtime=1x) to query the 2M backlog")
	}
	s := cachedFindingServer(2_000_000)
	for _, q := range findingBenchQueries {
		b.Run(q.name, func(b *testing.B) { benchQueryFindings(b, s, q.raw) })
	}
}

func benchQueryFindings(b *testing.B, s *Server, raw string) {
	v, _ := url.ParseQuery(raw)
	fq, err := parseFindingQuery(v, defaultFindingLimit, maxFindingLimit)
	if err != nil {
		b.Fatal(err)
	}
	ctx := b.Context()
	verbs := []string{"approve", "retry"}
	user := &User{Name: "bench", LoggedIn: true}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		page, err := s.queryFindings(ctx, fq, verbs, user)
		if err != nil {
			b.Fatal(err)
		}
		if len(page.Findings) > fq.limit {
			b.Fatalf("page = %d rows, limit %d", len(page.Findings), fq.limit)
		}
	}
}

// countingWriter counts bytes on their way to the void.
type countingWriter struct{ n int64 }

func (w *countingWriter) Write(p []byte) (int, error) { w.n += int64(len(p)); return len(p), nil }

// BenchmarkDatasetEncode measures serializing the largest findings page a
// client may ask for, compact against indented, reporting response bytes.
func BenchmarkDatasetEncode(b *testing.B) {
	s := cachedFindingServer(10_000)
	fq := findingQuery{limit: maxFindingLimit}
	ds, err := s.queryFindings(b.Context(), fq, []string{"approve"}, &User{Name: "bench"})
	if err != nil {
		b.Fatal(err)
	}
	for _, mode := range []string{"compact", "indented"} {
		b.Run("mode="+mode, func(b *testing.B) {
			b.ReportAllocs()
			var w countingWriter
			for b.Loop() {
				w.n = 0
				enc := json.NewEncoder(&w)
				if mode == "indented" {
					enc.SetIndent("", "  ")
				}
				if err := enc.Encode(ds); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(w.n), "bytes/op")
		})
	}
}

// BenchmarkSSEFanout measures publishing a change notification to K
// subscribers whose buffers are already full. That is the broker's worst
// case, where every send takes the non-blocking drop path.
func BenchmarkSSEFanout(b *testing.B) {
	for _, k := range []int{10, 100, 1_000} {
		b.Run(fmt.Sprintf("subscribers=%d", k), func(b *testing.B) {
			br := newBroker()
			for range k {
				ch := br.subscribe()
				for len(ch) < cap(ch) {
					ch <- eventFindingsChanged
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				br.publish(eventFindingsChanged)
			}
		})
	}
}

// TestComplexityQueryFindings asserts a first-page query stays near-linear:
// the per-finding cost at 100k may exceed the 10k cost by at most 30%. The
// page heap is bounded by the page size, so anything superlinear is a
// regression (a full sort crept back in, say).
func TestComplexityQueryFindings(t *testing.T) {
	if os.Getenv("PATCHY_BENCH_ASSERT") == "" {
		t.Skip("set PATCHY_BENCH_ASSERT=1 to run complexity assertions")
	}
	perFinding := func(n int) float64 {
		s := cachedFindingServer(n)
		res := testing.Benchmark(func(b *testing.B) { benchQueryFindings(b, s, "limit=50") })
		return float64(res.NsPerOp()) / float64(n)
	}
	perSmall, perLarge := perFinding(10_000), perFinding(100_000)
	ratio := perLarge / perSmall
	t.Logf("queryFindings ns/finding: 10k %.0f, 100k %.0f, ratio %.2f", perSmall, perLarge, ratio)
	if ratio > 1.3 {
		t.Errorf("per-finding query cost grows superlinearly: 100k/10k per-item ratio = %.2f, want <= 1.3", ratio)
	}
}
