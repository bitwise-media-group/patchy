// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Tier-1 microbenchmarks for the scheduler's slot picking, swept to the full
// 2M design target — Pick is pure in-memory arithmetic, so the whole backlog
// fits and the n log n curve is measured directly rather than extrapolated.

package schedule

import (
	"fmt"
	"math/rand/v2"
	"os"
	"testing"
	"time"
)

var benchNow = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

// benchCandidates builds n pending runs with deterministic priorities and
// queue times, ~1% expedited.
func benchCandidates(n int) []Candidate {
	r := rand.New(rand.NewPCG(1, uint64(n)))
	out := make([]Candidate, 0, n)
	for i := range n {
		out = append(out, Candidate{
			Name:      fmt.Sprintf("finding-%08d-rem-1", i),
			Priority:  int32(r.IntN(101)),
			QueuedAt:  benchNow.Add(-time.Duration(r.IntN(14*24*3600)) * time.Second),
			Expedited: r.IntN(100) == 0,
		})
	}
	return out
}

var benchAging = AgingPolicy{Interval: 24 * time.Hour, Cap: 25}

// BenchmarkPick measures ranking the whole pending set for a slot grant —
// what each scheduler pass over a brownfield backlog costs.
func BenchmarkPick(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 100_000, 2_000_000} {
		pending := benchCandidates(n)
		for _, slots := range []int{1, 3} {
			b.Run(fmt.Sprintf("pending=%d/slots=%d", n, slots), func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					if got := Pick(pending, slots, benchNow, benchAging); len(got) != slots {
						b.Fatalf("picked %d, want %d", len(got), slots)
					}
				}
			})
		}
	}
}

// TestComplexityPick asserts Pick stays n log n: 2M candidates may cost at
// most 30× what 100k does (20× items plus the log factor).
func TestComplexityPick(t *testing.T) {
	if os.Getenv("PATCHY_BENCH_ASSERT") == "" {
		t.Skip("set PATCHY_BENCH_ASSERT=1 to run complexity assertions")
	}
	small := benchCandidates(100_000)
	large := benchCandidates(2_000_000)
	rSmall := testing.Benchmark(func(b *testing.B) {
		for b.Loop() {
			Pick(small, 3, benchNow, benchAging)
		}
	})
	rLarge := testing.Benchmark(func(b *testing.B) {
		for b.Loop() {
			Pick(large, 3, benchNow, benchAging)
		}
	})
	ratio := float64(rLarge.NsPerOp()) / float64(rSmall.NsPerOp())
	t.Logf("pick ns/op: 100k %d, 2M %d, ratio %.2f", rSmall.NsPerOp(), rLarge.NsPerOp(), ratio)
	if ratio > 30 {
		t.Errorf("pick cost is superlinear: 2M/100k ratio = %.2f, want <= 30 (n log n)", ratio)
	}
}
