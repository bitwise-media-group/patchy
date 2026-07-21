// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package stats is the pure arithmetic behind the all-time FindingRollup
// objects: micro-USD cost parsing, per-scope delta computation, and
// exactly-once ledger application. Everything here is table-testable — the
// rollup reconciler supplies objects and writes results, this package never
// touches the cluster.
package stats

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
)

// maxLedger bounds the per-object exactly-once ledger.
const maxLedger = 512

// ParseCostMicroUSD converts a CRD decimal cost string into integer
// micro-USD without float arithmetic. Empty means zero.
func ParseCostMicroUSD(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	whole, frac, _ := strings.Cut(s, ".")
	if frac == "" && whole == "" {
		return 0, fmt.Errorf("stats: malformed cost %q", s)
	}
	var out int64
	for _, r := range whole {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("stats: malformed cost %q", s)
		}
		out = out*10 + int64(r-'0')
	}
	out *= 1_000_000
	// Right-pad/truncate the fraction to 6 digits (micro-USD).
	scale := int64(100_000)
	for i, r := range frac {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("stats: malformed cost %q", s)
		}
		if i >= 6 {
			break
		}
		out += int64(r-'0') * scale
		scale /= 10
	}
	return out, nil
}

// StageDelta is one agent run's contribution to a StageAggregate.
type StageDelta struct {
	// Stage is "investigation" or "remediation".
	Stage string
	// Outcome from the envelope vocabulary (+ "aborted").
	Outcome string
	// Succeeded marks an ok run (for remediation: ok and successful).
	Succeeded bool
	// Harness/Model executed the run; empty when the run died without one.
	Harness string
	Model   string
	// Token sums.
	InputTokens, OutputTokens, CacheReadTokens, CacheCreationTokens int64
	// CostMicroUSD spent.
	CostMicroUSD int64
	// ElapsedMilliseconds of wall clock.
	ElapsedMilliseconds int64
}

// StageDeltaFrom derives the delta from a child's stage result.
func StageDeltaFrom(stage string, st *v1alpha1.StageResult, succeeded bool) (StageDelta, error) {
	d := StageDelta{Stage: stage, Outcome: "aborted", Succeeded: succeeded}
	if st == nil {
		return d, nil
	}
	cost, err := ParseCostMicroUSD(st.Usage.CostUSD)
	if err != nil {
		return d, err
	}
	if st.Outcome != "" {
		d.Outcome = st.Outcome
	}
	d.Harness = st.Harness
	d.Model = st.Model
	d.InputTokens = st.Usage.InputTokens
	d.OutputTokens = st.Usage.OutputTokens
	d.CacheReadTokens = st.Usage.CacheReadTokens
	d.CacheCreationTokens = st.Usage.CacheCreationTokens
	d.CostMicroUSD = cost
	d.ElapsedMilliseconds = st.ElapsedMilliseconds
	return d, nil
}

// applyStage folds the delta into a bucket's stage aggregate.
func applyStage(b *v1alpha1.RollupBucket, d StageDelta) {
	if b.Stages == nil {
		b.Stages = map[string]v1alpha1.StageAggregate{}
	}
	agg := b.Stages[d.Stage]
	agg.Runs++
	if d.Succeeded {
		agg.Succeeded++
	}
	if agg.Outcomes == nil {
		agg.Outcomes = map[string]int64{}
	}
	agg.Outcomes[d.Outcome]++
	agg.InputTokens += d.InputTokens
	agg.OutputTokens += d.OutputTokens
	agg.CacheReadTokens += d.CacheReadTokens
	agg.CacheCreationTokens += d.CacheCreationTokens
	agg.CostMicroUSD += d.CostMicroUSD
	agg.ElapsedMilliseconds += d.ElapsedMilliseconds
	b.Stages[d.Stage] = agg
}

// FindingDelta is one finding's terminal contribution (total and repository
// scopes only — a finding has no single harness/model owner).
type FindingDelta struct {
	// Phase bucket to increment (kebab-free lowercase: remediated, failed,
	// dismissed, handedoff, deleted).
	Phase string
	// PrevPhase, when non-empty, is decremented first — the revival
	// correction (the finding was counted under PrevPhase, then revived and
	// completed again).
	PrevPhase string
	// Recommendation histogram key (remediate/ignore/manual); empty skips.
	Recommendation string
	// Attempts summed (investigation + remediation), counted only on the
	// first terminal entry (corrections pass zero to avoid double counts).
	Attempts int64
	// FirstCount marks the finding's first terminal entry (findings++).
	FirstCount bool
}

// applyFinding folds the delta into a bucket's finding-level counters.
func applyFinding(b *v1alpha1.RollupBucket, d FindingDelta) {
	if b.Phases == nil {
		b.Phases = map[string]int64{}
	}
	if d.FirstCount {
		b.Findings++
	}
	if d.PrevPhase != "" && b.Phases[d.PrevPhase] > 0 {
		b.Phases[d.PrevPhase]--
	}
	b.Phases[d.Phase]++
	if d.Recommendation != "" {
		if b.Recommendations == nil {
			b.Recommendations = map[string]int64{}
		}
		if d.FirstCount {
			b.Recommendations[d.Recommendation]++
		}
	}
	b.Attempts += d.Attempts
}

// PhaseKey maps a Finding phase onto its rollup histogram key.
func PhaseKey(p v1alpha1.Phase) string {
	return strings.ToLower(string(p))
}

// ScopeObjectName is the deterministic FindingRollup object name for a
// scope value: total, repo-<hash>, harness-<sanitized>, model-<sanitized>.
func ScopeObjectName(scope v1alpha1.RollupScope) string {
	switch scope.Type {
	case v1alpha1.ScopeTotal:
		return "total"
	case v1alpha1.ScopeRepository:
		sum := sha256.Sum256([]byte(scope.Key))
		return "repo-" + hex.EncodeToString(sum[:5])
	default:
		return string(scope.Type) + "-" + sanitizeName(scope.Key)
	}
}

// sanitizeName coerces an identifier into a DNS-1123 name fragment.
func sanitizeName(s string) string {
	b := []byte(strings.ToLower(s))
	for i, ch := range b {
		switch {
		case ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9', ch == '-':
		default:
			b[i] = '-'
		}
	}
	out := strings.Trim(string(b), "-")
	if out == "" {
		out = "x"
	}
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

// Apply folds one delta into a rollup status under the exactly-once ledger:
// it returns false (and mutates nothing) when ledgerKey was already applied.
// Exactly one of stage/finding may be non-nil. The monthly trend line is
// maintained only when month is non-empty (the total scope).
func Apply(
	st *v1alpha1.FindingRollupStatus, ledgerKey string,
	stage *StageDelta, finding *FindingDelta, now time.Time, month string,
) bool {
	if slices.Contains(st.Recent, ledgerKey) {
		return false
	}
	if st.SchemaVersion == 0 {
		st.SchemaVersion = 1
	}
	mt := metav1.NewTime(now.UTC())
	if st.FirstProcessed == nil {
		st.FirstProcessed = &mt
	}
	st.LastProcessed = &mt

	switch {
	case stage != nil:
		applyStage(&st.Bucket, *stage)
		if month != "" {
			m := monthlyOf(st, month)
			m.Runs++
			m.CostMicroUSD += stage.CostMicroUSD
			st.Monthly[month] = m
		}
	case finding != nil:
		applyFinding(&st.Bucket, *finding)
		if month != "" && finding.FirstCount {
			m := monthlyOf(st, month)
			m.Findings++
			st.Monthly[month] = m
		}
	}

	st.Recent = append(st.Recent, ledgerKey)
	if len(st.Recent) > maxLedger {
		st.Recent = st.Recent[len(st.Recent)-maxLedger:]
	}
	return true
}

// monthlyOf returns the month's bucket, allocating the map on first use.
func monthlyOf(st *v1alpha1.FindingRollupStatus, month string) v1alpha1.MonthlyBucket {
	if st.Monthly == nil {
		st.Monthly = map[string]v1alpha1.MonthlyBucket{}
	}
	return st.Monthly[month]
}
