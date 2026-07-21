// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package stats

import (
	"testing"
	"time"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
)

func TestParseCostMicroUSD(t *testing.T) {
	cases := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"", 0, false},
		{"0", 0, false},
		{"1.25", 1_250_000, false},
		{"1.250000", 1_250_000, false},
		{"0.000001", 1, false},
		{"12", 12_000_000, false},
		{"3.1415926", 3_141_592, false}, // truncated past micro precision
		{"1.2.3", 0, true},
		{"-1", 0, true},
		{"abc", 0, true},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := ParseCostMicroUSD(c.in)
			if (err != nil) != c.wantErr {
				t.Fatalf("ParseCostMicroUSD(%q) error = %v, wantErr %v", c.in, err, c.wantErr)
			}
			if got != c.want {
				t.Errorf("ParseCostMicroUSD(%q) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}

var applyClock = time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)

func TestApplyStageDelta(t *testing.T) {
	var st v1alpha1.FindingRollupStatus
	d := StageDelta{
		Stage: "investigation", Outcome: "ok", Succeeded: true,
		Harness: "claude", Model: "claude-sonnet-5",
		InputTokens: 100, OutputTokens: 50, CostMicroUSD: 1_250_000, ElapsedMilliseconds: 60000,
	}
	if !Apply(&st, "i:uid-1", &d, nil, applyClock, "2026-07") {
		t.Fatal("Apply returned false on first application")
	}
	agg := st.Bucket.Stages["investigation"]
	if agg.Runs != 1 || agg.Succeeded != 1 || agg.Outcomes["ok"] != 1 {
		t.Errorf("aggregate = %+v", agg)
	}
	if agg.CostMicroUSD != 1_250_000 || agg.InputTokens != 100 {
		t.Errorf("sums = %+v", agg)
	}
	if st.Monthly["2026-07"].Runs != 1 || st.Monthly["2026-07"].CostMicroUSD != 1_250_000 {
		t.Errorf("monthly = %+v", st.Monthly)
	}

	// Exactly-once: the same ledger key mutates nothing.
	if Apply(&st, "i:uid-1", &d, nil, applyClock, "2026-07") {
		t.Fatal("Apply returned true on duplicate ledger key")
	}
	if st.Bucket.Stages["investigation"].Runs != 1 {
		t.Errorf("runs = %d after dup, want 1", st.Bucket.Stages["investigation"].Runs)
	}
}

func TestApplyFindingDeltaAndRevivalCorrection(t *testing.T) {
	var st v1alpha1.FindingRollupStatus
	first := FindingDelta{
		Phase: "handedoff", Recommendation: "manual", Attempts: 2, FirstCount: true,
	}
	if !Apply(&st, "f:uid-1:1", nil, &first, applyClock, "2026-07") {
		t.Fatal("first Apply returned false")
	}
	if st.Bucket.Findings != 1 || st.Bucket.Phases["handedoff"] != 1 || st.Bucket.Attempts != 2 {
		t.Errorf("bucket = %+v", st.Bucket)
	}

	// Revival: handed off → remediated. Correction decrements the old phase.
	correction := FindingDelta{Phase: "remediated", PrevPhase: "handedoff"}
	if !Apply(&st, "f:uid-1:2", nil, &correction, applyClock, "2026-07") {
		t.Fatal("correction Apply returned false")
	}
	if st.Bucket.Findings != 1 {
		t.Errorf("findings = %d after correction, want 1 (no double count)", st.Bucket.Findings)
	}
	if st.Bucket.Phases["handedoff"] != 0 || st.Bucket.Phases["remediated"] != 1 {
		t.Errorf("phases = %v, want handedoff 0 / remediated 1", st.Bucket.Phases)
	}
	if st.Monthly["2026-07"].Findings != 1 {
		t.Errorf("monthly findings = %d, want 1", st.Monthly["2026-07"].Findings)
	}
}

func TestApplyLedgerEviction(t *testing.T) {
	var st v1alpha1.FindingRollupStatus
	d := StageDelta{Stage: "investigation", Outcome: "ok"}
	for i := range maxLedger + 10 {
		key := "i:" + string(rune('a'+i%26)) + string(rune('0'+i%10)) + itoa(i)
		Apply(&st, key, &d, nil, applyClock, "")
	}
	if len(st.Recent) != maxLedger {
		t.Errorf("ledger = %d entries, want capped at %d", len(st.Recent), maxLedger)
	}
	if st.Bucket.Stages["investigation"].Runs != int64(maxLedger+10) {
		t.Errorf("runs = %d, want %d", st.Bucket.Stages["investigation"].Runs, maxLedger+10)
	}
}

func itoa(i int) string {
	return time.Duration(i).String()
}

func TestScopeObjectName(t *testing.T) {
	cases := []struct {
		scope v1alpha1.RollupScope
		want  string
	}{
		{v1alpha1.RollupScope{Type: v1alpha1.ScopeTotal}, "total"},
		{v1alpha1.RollupScope{Type: v1alpha1.ScopeHarness, Key: "claude"}, "harness-claude"},
		{v1alpha1.RollupScope{Type: v1alpha1.ScopeModel, Key: "claude-sonnet-5"}, "model-claude-sonnet-5"},
		{v1alpha1.RollupScope{Type: v1alpha1.ScopeModel, Key: "Weird/Model:v1"}, "model-weird-model-v1"},
	}
	for _, c := range cases {
		if got := ScopeObjectName(c.scope); got != c.want {
			t.Errorf("ScopeObjectName(%+v) = %q, want %q", c.scope, got, c.want)
		}
	}
	repo := ScopeObjectName(v1alpha1.RollupScope{Type: v1alpha1.ScopeRepository, Key: "acme/orders"})
	if len(repo) != len("repo-")+10 {
		t.Errorf("repo scope name = %q, want repo-<hex10>", repo)
	}
}
