// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Tier-1 microbenchmarks for the rollup arithmetic. Apply runs inside every
// conflict-retry write to the single `total` FindingRollup, so its cost sits
// on the terminal-drain critical path — though at µs scale it is dwarfed by
// the API round-trips the drain ceiling actually comes from.

package stats

import (
	"fmt"
	"testing"
	"time"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
)

var benchNow = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

// benchStatus is a rollup status with ledger entries, both stage buckets,
// and months of trend line — the long-lived deployment shape.
func benchStatus(ledger int) *v1alpha1.FindingRollupStatus {
	st := &v1alpha1.FindingRollupStatus{
		SchemaVersion: v1alpha1.RollupSchemaVersion,
		Bucket: v1alpha1.RollupBucket{
			Findings:        1000,
			Phases:          map[string]int64{"remediated": 600, "dismissed": 200, "handedoff": 150, "failed": 50},
			Recommendations: map[string]int64{"remediate": 700, "ignore": 200, "manual": 100},
			Attempts:        1500,
			Stages: map[string]v1alpha1.StageAggregate{
				"investigation": {Runs: 1000, Succeeded: 900, Outcomes: map[string]int64{"ok": 900, "timeout": 100}},
				"remediation":   {Runs: 700, Succeeded: 600, Outcomes: map[string]int64{"ok": 600, "timeout": 100}},
			},
		},
		Monthly: map[string]v1alpha1.MonthlyBucket{},
	}
	for m := range 24 {
		st.Monthly[benchNow.AddDate(0, -m, 0).Format("2006-01")] = v1alpha1.MonthlyBucket{Findings: 10, Runs: 20}
	}
	for k := range ledger {
		st.Recent = append(st.Recent, fmt.Sprintf("i:seed-%d", k))
	}
	return st
}

// BenchmarkApply measures folding one delta into the rollup: the ledger
// membership scan (0 vs the full 512), then the bucket arithmetic, for both
// delta kinds.
func BenchmarkApply(b *testing.B) {
	stage := &StageDelta{
		Stage: "remediation", Outcome: "ok", Succeeded: true,
		Harness: "claude", Model: "anthropic/claude-sonnet-5",
		InputTokens: 50000, OutputTokens: 8000, CacheReadTokens: 200000,
		CostMicroUSD: 1_250_000, ElapsedMilliseconds: 300000, Turns: 15,
		Estimate: &EstimateDelta{PredictedTurns: 20, ActualTurns: 15, PredictedOutputTokens: 10000, ActualOutputTokens: 8000},
	}
	finding := &FindingDelta{Phase: "remediated", Recommendation: "remediate", Attempts: 2, Count: true, First: true}
	for _, ledger := range []int{0, maxLedger} {
		b.Run(fmt.Sprintf("kind=stage/ledger=%d", ledger), func(b *testing.B) {
			benchApply(b, ledger, stage, nil)
		})
		b.Run(fmt.Sprintf("kind=finding/ledger=%d", ledger), func(b *testing.B) {
			benchApply(b, ledger, nil, finding)
		})
	}
}

func benchApply(b *testing.B, ledger int, stage *StageDelta, finding *FindingDelta) {
	st := benchStatus(ledger)
	month := benchNow.Format("2006-01")
	// Unique keys per iteration: a repeated key short-circuits on the ledger
	// and skips the arithmetic under measurement.
	keys := make([]string, 0, 1024)
	for k := range 1024 {
		keys = append(keys, fmt.Sprintf("i:bench-%d", k))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		if !Apply(st, keys[i%len(keys)], stage, finding, benchNow, month) {
			// The ledger wrapped and evicted this key's earlier entry — the
			// 512-cap guarantees keys 1024 apart never collide.
			b.Fatal("delta was refused as a duplicate")
		}
	}
}
