// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Tier-1 microbenchmarks for the status-server dataset path: projecting one
// Finding, assembling the list dataset at backlog scale, encoding it, the
// one-finding detail build, and the SSE fan-out. Run via `mise run bench`; the TestComplexity* assertions
// are opt-in (PATCHY_BENCH_ASSERT=1). The 2M-findings dataset build needs
// ~16GB and is additionally gated on PATCHY_BENCH_HUGE=1 (run it with
// -benchtime=1x).

package web

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/kube"
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

// benchServer builds a Server over a fake client holding n findings, n/10
// rollups, and n/2 investigation children. The list build must read findings
// and rollups only — the children are seeded so a reintroduced child join
// would show up in the BuildDataset numbers. The detail path is NOT benched
// here: the fake client deep-copies every child per List regardless of the
// field index, so it can only show the scan the real informer cache avoids
// (the e2e load test measures the real thing).
func benchServer(tb testing.TB, n int) *Server {
	tb.Helper()
	o := loadgen.Opts{PhaseMix: benchPhaseMix, AlertsPerFinding: 2}
	rollups := max(n/10, 1)
	children := n / 2
	c := fake.NewClientBuilder().
		WithScheme(kube.Scheme()).
		WithLists(
			&v1alpha1.FindingList{Items: loadgen.Findings(n, o)},
			&v1alpha1.FindingRollupList{Items: loadgen.Rollups(rollups)},
			&v1alpha1.InvestigationList{Items: loadgen.Investigations(children, o)},
		).
		WithIndex(&v1alpha1.Investigation{}, RunFindingIndex, RunFindingIndexer).
		WithIndex(&v1alpha1.Remediation{}, RunFindingIndex, RunFindingIndexer).
		Build()
	return NewServer(c, loadgen.Namespace, nil, nil, nil)
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

// BenchmarkProjectFinding measures flattening one CR onto the wire type —
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

// BenchmarkBuildDataset assembles the trimmed list payload at each backlog
// size: two Lists, the summary projection, and the newest-first sort.
func BenchmarkBuildDataset(b *testing.B) {
	for _, n := range []int{10_000, 100_000} {
		b.Run(fmt.Sprintf("findings=%d", n), func(b *testing.B) {
			benchBuildDataset(b, n)
		})
	}
}

// BenchmarkBuildDatasetHuge is the full 2M design target. ~16GB of working
// set: opt in with PATCHY_BENCH_HUGE=1 and run with -benchtime=1x.
func BenchmarkBuildDatasetHuge(b *testing.B) {
	if os.Getenv("PATCHY_BENCH_HUGE") == "" {
		b.Skip("set PATCHY_BENCH_HUGE=1 (and -benchtime=1x) to build the 2M dataset")
	}
	benchBuildDataset(b, 2_000_000)
}

func benchBuildDataset(b *testing.B, n int) {
	s := benchServer(b, n)
	ctx := b.Context()
	verbs := []string{"approve", "retry"}
	user := &User{Name: "bench", LoggedIn: true}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		ds, err := s.buildDataset(ctx, true, verbs, user)
		if err != nil {
			b.Fatal(err)
		}
		if len(ds.Findings) != n {
			b.Fatalf("findings = %d, want %d", len(ds.Findings), n)
		}
	}
}

// countingWriter counts bytes on their way to the void.
type countingWriter struct{ n int64 }

func (w *countingWriter) Write(p []byte) (int, error) { w.n += int64(len(p)); return len(p), nil }

// BenchmarkDatasetEncode measures serializing the assembled dataset —
// compact against indented, response bytes reported — over 10k findings.
func BenchmarkDatasetEncode(b *testing.B) {
	s := benchServer(b, 10_000)
	ds, err := s.buildDataset(b.Context(), true, []string{"approve"}, &User{Name: "bench"})
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
// subscribers whose buffers are already full — the broker's worst case,
// where every send takes the non-blocking drop path.
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
				br.publish()
			}
		})
	}
}

// TestComplexityBuildDataset asserts the dataset build stays near-linear:
// the per-finding cost at 100k may exceed the 10k cost by at most 30%
// (n log n from the sort is tolerated; anything superlinear beyond that is
// a regression).
func TestComplexityBuildDataset(t *testing.T) {
	if os.Getenv("PATCHY_BENCH_ASSERT") == "" {
		t.Skip("set PATCHY_BENCH_ASSERT=1 to run complexity assertions")
	}
	small := testing.Benchmark(func(b *testing.B) { benchBuildDataset(b, 10_000) })
	large := testing.Benchmark(func(b *testing.B) { benchBuildDataset(b, 100_000) })
	perSmall := float64(small.NsPerOp()) / 10_000
	perLarge := float64(large.NsPerOp()) / 100_000
	ratio := perLarge / perSmall
	t.Logf("buildDataset ns/finding: 10k %.0f, 100k %.0f, ratio %.2f", perSmall, perLarge, ratio)
	if ratio > 1.3 {
		t.Errorf("per-finding dataset cost grows superlinearly: 100k/10k per-item ratio = %.2f, want <= 1.3", ratio)
	}
}
