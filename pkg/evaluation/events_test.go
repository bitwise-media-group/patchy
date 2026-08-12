// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package evaluation

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEventRoundTrip(t *testing.T) {
	avg := 12.5
	hits := 3
	tests := []struct {
		name string
		ev   Event
	}{
		{"unit_started", Event{
			Type: TypeUnitStarted,
			Unit: &UnitRef{Plugin: "workflow", Skill: "commit", Key: "anthropic/claude-sonnet-5", Kind: "evals"},
			Item: &ItemEvent{Total: 7, Runs: 1, Mode: "run"},
		}},
		{"item_done", Event{
			Type: TypeItemDone,
			Unit: &UnitRef{Skill: "commit", Key: "anthropic/claude-sonnet-5", Kind: "triggers"},
			Item: &ItemEvent{
				Index: 2, Label: "commit my changes", Status: "pass",
				Metrics: &ItemMetrics{Hits: &hits, AvgRunSeconds: &avg},
			},
		}},
		{"unit_finished", Event{
			Type: TypeUnitFinished,
			Unit: &UnitRef{Skill: "commit", Key: "anthropic/claude-sonnet-5", Kind: "evals"},
			Sum:  &UnitSummary{Executed: true, Passed: 6, Errored: 1, Total: 7, AvgRunSeconds: &avg},
		}},
		{"warn", Event{Type: TypeWarn, Msg: "judge unavailable: budget"}},
		{"result", Event{
			Type: TypeResult,
			Unit: &UnitRef{Skill: "commit", Key: "anthropic/claude-sonnet-5", Kind: "evals"},
			Result: &UnitResult{
				Tier: 2, Model: "anthropic/claude-sonnet-5", Harness: "claude", Failed: false,
				Summary: ResultSummary{
					CasesPassed: 6, CasesErrored: 1,
					Cases:      []CaseStatus{{ID: "basic-commit", Passed: true}},
					TokenUsage: TokenUsage{InputTokens: 120000, OutputTokens: 9000, CostUSD: 1.25},
					ElapsedMS:  654321, Outcome: "ok",
				},
				Entry: json.RawMessage(`{"schema":5,"results":[]}`),
			},
		}},
		{"fatal", Event{Type: TypeFatal, Error: "bundle missing evals/commit"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			line, err := tt.ev.Encode()
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			if !strings.HasPrefix(line, EventPrefix) {
				t.Fatalf("Encode line lacks prefix: %q", line)
			}
			got, ok := Decode([]byte(line))
			if !ok {
				t.Fatalf("Decode(%q) not ok", line)
			}
			want := tt.ev
			want.V = EventVersion
			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(want)
			if string(gotJSON) != string(wantJSON) {
				t.Errorf("round trip mismatch:\n got %s\nwant %s", gotJSON, wantJSON)
			}
		})
	}
}

func TestDecodeFindsPrefixAnywhere(t *testing.T) {
	line, err := Event{Type: TypeWarn, Msg: "hello"}.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	wrapped := "2026-08-12T10:00:00.000Z stdout F " + line
	if _, ok := Decode([]byte(wrapped)); !ok {
		t.Error("Decode(timestamped line) not ok, want ok")
	}
}

func TestDecodeRejectsJunk(t *testing.T) {
	for _, line := range []string{
		"",
		"plain stderr chatter",
		EventPrefix + "{not json",
		EventPrefix + `{"v":99,"type":"warn"}`,
		EventPrefix + `{"v":1}`,
	} {
		if _, ok := Decode([]byte(line)); ok {
			t.Errorf("Decode(%q) ok, want rejected", line)
		}
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		in   string
		max  int
		want string
	}{
		{"short", 100, "short"},
		{"abcdef", 6, "abcdef"},
		{"abcdefg", 6, "abc…"},
		{"héllo wörld", 8, "héll…"},
	}
	for _, tt := range tests {
		if got := Truncate(tt.in, tt.max); got != tt.want {
			t.Errorf("Truncate(%q, %d) = %q, want %q", tt.in, tt.max, got, tt.want)
		}
		if got := Truncate(tt.in, tt.max); len(got) > tt.max {
			t.Errorf("Truncate(%q, %d) yielded %d bytes, want <= max", tt.in, tt.max, len(got))
		}
	}
}
