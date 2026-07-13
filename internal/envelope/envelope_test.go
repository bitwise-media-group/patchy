// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package envelope

import (
	"reflect"
	"strings"
	"testing"
)

func testEvent() Event {
	return Event{
		Type:  TypeClassification,
		Repo:  "acme/shop",
		Issue: 123,
		Classification: &Classification{
			Stage: Stage{
				Outcome: OutcomeOK, Harness: "claude", Model: "claude-sonnet-5",
				SessionID: "a1b2c3d4-0000-0000-0000-000000000000", NumTurns: 9,
				Usage:          Usage{InputTokens: 1200, OutputTokens: 5600, CostUSD: 0.42},
				ElapsedSeconds: 93.1,
			},
			ReportMarkdown:   "report",
			Recommendation:   "remediate",
			Priority:         "high",
			Severity:         "high",
			Confidence:       0.85,
			RemediationModel: "claude-sonnet-5",
			MaxTurns:         40,
			TokenBudget:      200000,
			WillRemediate:    true,
		},
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	want := testEvent()
	line, err := want.Encode()
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	if !strings.HasPrefix(line, Prefix) {
		t.Fatalf("line %q missing prefix", line)
	}
	if strings.ContainsRune(line, '\n') {
		t.Fatal("encoded event spans multiple lines")
	}

	got, ok := Decode([]byte(line))
	if !ok {
		t.Fatal("Decode() ok = false")
	}
	want.V = Version
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round-trip = %+v, want %+v", got, want)
	}
}

func TestDecodeWrappedLine(t *testing.T) {
	// Log pipelines may prepend timestamps; Decode finds the prefix anywhere.
	line, err := testEvent().Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, ok := Decode([]byte("2026-07-13T12:00:00Z " + line))
	if !ok || got.Issue != 123 {
		t.Errorf("Decode(wrapped) = %+v, %v; want event, true", got, ok)
	}
}

func TestDecodeRejects(t *testing.T) {
	tests := []struct {
		name string
		line string
	}{
		{"plain log line", "level=INFO msg=hello"},
		{"prefix with bad json", Prefix + "{nope"},
		{"wrong version", Prefix + `{"v":99,"type":"classification"}`},
		{"missing type", Prefix + `{"v":1}`},
		{"empty", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, ok := Decode([]byte(tt.line)); ok {
				t.Error("Decode() ok = true, want false")
			}
		})
	}
}

func TestFatalEvent(t *testing.T) {
	line, err := (Event{Type: TypeFatal, Repo: "acme/shop", Issue: 5, Error: "workspace missing"}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, ok := Decode([]byte(line))
	if !ok || got.Type != TypeFatal || got.Error != "workspace missing" {
		t.Errorf("Decode(fatal) = %+v, %v", got, ok)
	}
}
