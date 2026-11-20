// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package model

import (
	"slices"
	"strings"
	"testing"
)

func TestParseClaudeID(t *testing.T) {
	tests := []struct {
		bare         string
		family       string
		major, minor int
		ok           bool
	}{
		{"claude-opus-5-5", "opus", 5, 5, true},
		{"claude-haiku-4-5", "haiku", 4, 5, true},
		{"claude-fable-5", "fable", 5, 0, true},
		{"claude-mythos-5-2", "mythos", 5, 2, true},
		{"claude-newfamily-7", "newfamily", 7, 0, true},
		{"claude-3-5-sonnet-20241022", "", 0, 0, false}, // version-first scheme
		{"claude-opus-4-20250514", "", 0, 0, false},     // dated snapshot
		{"claude-opus-4-5-20251101", "", 0, 0, false},
		{"claude-opus", "", 0, 0, false}, // alias shape, no version
		{"claude-Opus-5", "", 0, 0, false},
		{"gpt-5", "", 0, 0, false},
		{"", "", 0, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.bare, func(t *testing.T) {
			family, major, minor, ok := ParseClaudeID(tt.bare)
			if ok != tt.ok || family != tt.family || major != tt.major || minor != tt.minor {
				t.Errorf("ParseClaudeID(%q) = %q, %d, %d, %v; want %q, %d, %d, %v",
					tt.bare, family, major, minor, ok, tt.family, tt.major, tt.minor, tt.ok)
			}
		})
	}
}

func TestIsAlias(t *testing.T) {
	for id, want := range map[string]bool{
		"anthropic/claude-opus":     true,
		"anthropic/claude-mythos":   true,
		"anthropic/claude-opus-5-5": false,
		"claude-opus":               false,
		"openai/gpt":                false,
	} {
		if got := IsAlias(id); got != want {
			t.Errorf("IsAlias(%q) = %v, want %v", id, got, want)
		}
	}
}

// TestResolve covers the three tiers (exact, alias → newest registered
// version, and pattern synthesis) plus the misses.
func TestResolve(t *testing.T) {
	tests := []struct {
		id     string
		wantID string // "" means not ok
		synth  bool
	}{
		{"anthropic/claude-sonnet-5", "anthropic/claude-sonnet-5", false}, // exact, superseded but registered
		{"anthropic/claude-opus", "anthropic/claude-opus-5-5", false},
		{"anthropic/claude-sonnet", "anthropic/claude-sonnet-5-5", false},
		{"anthropic/claude-haiku", "anthropic/claude-haiku-5-5", false},
		{"anthropic/claude-fable", "anthropic/claude-fable-5-2", false},
		{"anthropic/claude-mythos", "anthropic/claude-mythos-5-2", false},
		{"anthropic/claude-opus-6", "anthropic/claude-opus-6", true},
		{"anthropic/claude-sonnet-6-1", "anthropic/claude-sonnet-6-1", true},
		{"anthropic/claude-unknownfamily", "", false}, // alias of no registered family
		{"anthropic/claude-3-5-sonnet-20241022", "", false},
		{"openai/gpt-7", "", false}, // the pattern tier is Anthropic-only
		{"anthropic/gpt-5", "", false},
		{"claude-opus-6", "", false}, // not provider-qualified
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			m, ok := Lookup(tt.id)
			if ok != (tt.wantID != "") {
				t.Fatalf("Lookup(%q) ok = %v, want %v", tt.id, ok, tt.wantID != "")
			}
			if !ok {
				return
			}
			if m.ID != tt.wantID {
				t.Errorf("Lookup(%q).ID = %q, want %q", tt.id, m.ID, tt.wantID)
			}
			if Synthesized(m) != tt.synth {
				t.Errorf("Synthesized(%q) = %v, want %v", tt.id, Synthesized(m), tt.synth)
			}
		})
	}
}

// TestSynthesizedModel pins the pattern-tier shape: claude only, preferred
// claude, its bare id as the CLI id, and no pricing.
func TestSynthesizedModel(t *testing.T) {
	m, ok := Lookup("anthropic/claude-opus-6-1")
	if !ok {
		t.Fatal("anthropic/claude-opus-6-1 did not resolve")
	}
	if got := m.SupportedHarnessIDs(); !slices.Equal(got, []string{HarnessClaude}) {
		t.Errorf("supported = %v, want [claude]", got)
	}
	if id, _ := m.CLIModelID(HarnessClaude); id != "claude-opus-6-1" {
		t.Errorf("claude CLI id = %q, want claude-opus-6-1", id)
	}
	if m.Preferred != HarnessClaude || m.ProviderID != ProviderAnthropic {
		t.Errorf("preferred/provider = %q/%q, want claude/anthropic", m.Preferred, m.ProviderID)
	}
	if m.Name != "Claude Opus 6.1" {
		t.Errorf("Name = %q, want Claude Opus 6.1", m.Name)
	}
	if m.InputUSD != nil || m.CachedInputUSD != nil || m.OutputUSD != nil {
		t.Error("synthesized model carries pricing, want none")
	}
	if got := UsageCostUSD(m, 100, 100, 100, 100); got != nil {
		t.Errorf("UsageCostUSD(synthesized) = %v, want nil", *got)
	}
}

// TestResolveAliasOrdering checks the alias picks by (major, minor), not by
// registry order or string order.
func TestResolveAliasOrdering(t *testing.T) {
	models := []Model{
		{ID: "anthropic/claude-opus-10", ProviderID: ProviderAnthropic},
		{ID: "anthropic/claude-opus-9-9", ProviderID: ProviderAnthropic},
		{ID: "anthropic/claude-opus-10-1", ProviderID: ProviderAnthropic},
		{ID: "anthropic/claude-sonnet-11", ProviderID: ProviderAnthropic},
	}
	m, ok := Resolve(models, "anthropic/claude-opus")
	if !ok || m.ID != "anthropic/claude-opus-10-1" {
		t.Errorf("Resolve(opus alias) = %q, %v; want anthropic/claude-opus-10-1", m.ID, ok)
	}
}

func TestCanonicalize(t *testing.T) {
	got, err := Canonicalize([]string{
		"anthropic/claude-sonnet", " anthropic/claude-opus ", "anthropic/claude-sonnet-5-5", "",
		"anthropic/claude-opus-6", "anthropic/claude-sonnet-5",
	})
	if err != nil {
		t.Fatalf("Canonicalize: %v", err)
	}
	want := []string{
		"anthropic/claude-sonnet-5-5", "anthropic/claude-opus-5-5", "anthropic/claude-opus-6", "anthropic/claude-sonnet-5",
	}
	if !slices.Equal(got, want) {
		t.Errorf("Canonicalize = %v, want %v", got, want)
	}

	for _, bad := range []string{"anthropic/claude-nosuchfamily", "openai/gpt-7", "claude-3-5-sonnet-20241022"} {
		if _, err := Canonicalize([]string{"anthropic/claude-opus", bad}); err == nil ||
			!strings.Contains(err.Error(), bad) {
			t.Errorf("Canonicalize(%q) error = %v, want one naming it", bad, err)
		}
	}
}

func TestConcrete(t *testing.T) {
	for id, want := range map[string]string{
		"anthropic/claude-sonnet":   "anthropic/claude-sonnet-5-5",
		"anthropic/claude-opus-5":   "anthropic/claude-opus-5",
		"openai/not-a-model":        "openai/not-a-model", // unchanged, never fails
		"anthropic/claude-opus-6-2": "anthropic/claude-opus-6-2",
	} {
		if got := Concrete(id); got != want {
			t.Errorf("Concrete(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestWithResolved(t *testing.T) {
	base := Builtins()
	got := WithResolved(base, "anthropic/claude-opus", "anthropic/claude-opus-6", "anthropic/claude-opus-6",
		"openai/gpt-5.6-sol", "openai/nope")
	if len(got) != len(base)+1 {
		t.Fatalf("WithResolved added %d models, want 1 (the pattern id, once)", len(got)-len(base))
	}
	m, ok := ModelByID(got, "anthropic/claude-opus-6")
	if !ok || !Synthesized(m) {
		t.Errorf("anthropic/claude-opus-6 missing or not synthesized in %v", got)
	}
	if len(Builtins()) != len(base) {
		t.Error("WithResolved modified its input")
	}
}
