// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package model

import (
	"strings"
	"testing"
)

// TestBuiltinInvariants guards the registry's structural rules: ids are
// provider-qualified, the ProviderID prefix matches the id, the harness in each
// Supported map is one the registry knows, every model is supported by at least
// one harness, and Preferred is always one of those harnesses. A bad
// Supported/Preferred entry would silently send the wrong --model string to a
// CLI, so these are load-bearing.
func TestBuiltinInvariants(t *testing.T) {
	seen := map[string]bool{}
	for _, m := range builtins() {
		if seen[m.ID] {
			t.Errorf("duplicate model id %q", m.ID)
		}
		seen[m.ID] = true

		prov, rest, ok := strings.Cut(m.ID, "/")
		if !ok || rest == "" {
			t.Errorf("model id %q is not provider-qualified", m.ID)
		}
		if prov != m.ProviderID {
			t.Errorf("model %q: id prefix %q != ProviderID %q", m.ID, prov, m.ProviderID)
		}
		if !IsProviderID(m.ProviderID) {
			t.Errorf("model %q: unknown ProviderID %q", m.ID, m.ProviderID)
		}
		if len(m.Supported) == 0 {
			t.Errorf("model %q: no supported harness", m.ID)
		}
		for h := range m.Supported {
			if !IsKnownHarnessID(h) {
				t.Errorf("model %q: unknown supported harness %q", m.ID, h)
			}
		}
		if _, ok := m.Supported[m.Preferred]; !ok {
			t.Errorf("model %q: Preferred %q not in Supported %v", m.ID, m.Preferred, m.Supported)
		}
		// Every builtin carries published rates. Pricing is optional in the
		// type (a config override may name a model with none), but a builtin
		// that lost them would silently record zero-cost runs for the codex
		// harness, which reports no cost of its own.
		if m.InputUSD == nil || m.OutputUSD == nil {
			t.Errorf("model %q: missing pricing (%v/%v)", m.ID, m.InputUSD, m.OutputUSD)
		}
		// Both vendors publish a cache-read rate near a tenth of base input,
		// and cache reads dominate a long agent session, so a builtin that
		// omitted one would price them 10x over.
		if m.CachedInputUSD == nil {
			t.Errorf("model %q: missing cached-input rate", m.ID)
		}
	}
}

func TestModelIdentity(t *testing.T) {
	m, ok := ModelByID(builtins(), "anthropic/claude-sonnet-5")
	if !ok {
		t.Fatal("anthropic/claude-sonnet-5 missing from registry")
	}
	if m.Key() != "anthropic/claude-sonnet-5" {
		t.Errorf("Key() = %q, want anthropic/claude-sonnet-5", m.Key())
	}
	if m.BareID() != "claude-sonnet-5" {
		t.Errorf("BareID() = %q, want claude-sonnet-5", m.BareID())
	}
	if id, ok := m.CLIModelID(HarnessClaude); !ok || id != "claude-sonnet-5" {
		t.Errorf("claude CLI id = %q (%v), want claude-sonnet-5", id, ok)
	}
	if _, ok := m.CLIModelID(HarnessCodex); ok {
		t.Error("claude-sonnet-5 should not be supported by codex")
	}
	if m.Preferred != HarnessClaude {
		t.Errorf("Preferred = %q, want claude", m.Preferred)
	}
}

func TestCodexModel(t *testing.T) {
	m, ok := ModelByID(builtins(), "openai/gpt-5.3-codex")
	if !ok {
		t.Fatal("openai/gpt-5.3-codex missing from registry")
	}
	if id, ok := m.CLIModelID(HarnessCodex); !ok || id != "gpt-5.3-codex" {
		t.Errorf("codex CLI id = %q (%v), want gpt-5.3-codex", id, ok)
	}
	if m.Preferred != HarnessCodex {
		t.Errorf("Preferred = %q, want codex", m.Preferred)
	}
	if m.InputUSD == nil || m.CachedInputUSD == nil || m.OutputUSD == nil {
		t.Fatalf("pricing = %v/%v/%v, want 1.75/0.175/14.00 per MTok",
			m.InputUSD, m.CachedInputUSD, m.OutputUSD)
	}
	if *m.InputUSD != 1.75 || *m.CachedInputUSD != 0.175 || *m.OutputUSD != 14.00 {
		t.Errorf("pricing = %v/%v/%v, want 1.75/0.175/14.00 per MTok",
			*m.InputUSD, *m.CachedInputUSD, *m.OutputUSD)
	}
}

// TestCopilotSupportsEveryModel pins Copilot's role in the registry: it brokers
// both vendors, so every model must name it as a supported harness, and it must
// never be a model's Preferred — a vendor-native harness always wins when both
// are enabled. It also guards the id divergence Supported exists to record:
// Copilot spells Anthropic point releases with a dot.
func TestCopilotSupportsEveryModel(t *testing.T) {
	wantCLIID := map[string]string{
		"anthropic/claude-haiku-4-5": "claude-haiku-4.5",
		"anthropic/claude-sonnet-5":  "claude-sonnet-5",
		"anthropic/claude-opus-5":    "claude-opus-5",
		"anthropic/claude-fable-5":   "claude-fable-5",
		"openai/gpt-5.3-codex":       "gpt-5.3-codex",
		"openai/gpt-5.6-luna":        "gpt-5.6-luna",
		"openai/gpt-5.6-terra":       "gpt-5.6-terra",
		"openai/gpt-5.6-sol":         "gpt-5.6-sol",
	}
	models := builtins()
	if len(models) != len(wantCLIID) {
		t.Fatalf("registry has %d models, the copilot expectations cover %d — update both together",
			len(models), len(wantCLIID))
	}
	for _, m := range models {
		id, ok := m.CLIModelID(HarnessCopilot)
		if !ok {
			t.Errorf("model %q is not supported by copilot", m.ID)
			continue
		}
		if want := wantCLIID[m.ID]; id != want {
			t.Errorf("model %q copilot CLI id = %q, want %q", m.ID, id, want)
		}
		if m.Preferred == HarnessCopilot {
			t.Errorf("model %q prefers copilot, want its vendor-native harness", m.ID)
		}
	}
}

func TestUsageCostUSD(t *testing.T) {
	m, _ := ModelByID(builtins(), "anthropic/claude-sonnet-5") // 3 / 0.30 / 15 per MTok
	// Fresh input and cache writes at 3/MTok, cache reads at the 0.30/MTok
	// cache-read rate, output at 15/MTok:
	// (1_000_000 + 200_000)/1e6*3 + 500_000/1e6*0.30 + 100_000/1e6*15
	//   = 3.6 + 0.15 + 1.5 = 5.25
	got := UsageCostUSD(m, 1_000_000, 500_000, 200_000, 100_000)
	if got == nil || *got != 5.25 {
		t.Errorf("UsageCostUSD = %v, want 5.25", got)
	}

	// Without a published cache-read rate, cache reads fall back to the base
	// input rate: (100 + 100)/1e6*3 + 100/1e6*3 + 50/1e6*15 = 0.00165.
	noCache := m
	noCache.CachedInputUSD = nil
	if got := UsageCostUSD(noCache, 100, 100, 100, 50); got == nil || *got != 0.00165 {
		t.Errorf("UsageCostUSD(no cached rate) = %v, want 0.00165", got)
	}
	// A model without published pricing prices to nil. Every builtin is
	// priced, so this is the config-override case: a synthetic model whose
	// vendor publishes no per-token rates.
	unpriced := Model{
		ID: "openai/private-preview", ProviderID: ProviderOpenAI, Name: "Private Preview",
		Supported: map[string]string{HarnessCodex: "private-preview"}, Preferred: HarnessCodex,
	}
	if got := UsageCostUSD(unpriced, 100, 0, 0, 50); got != nil {
		t.Errorf("UsageCostUSD(unpriced) = %v, want nil", *got)
	}
}

// TestAllModelsOverride replaces one provider's matrix and leaves the others.
func TestAllModelsOverride(t *testing.T) {
	override := map[string][]Model{
		ProviderOpenAI: {{
			ID: "openai/gpt-6", ProviderID: ProviderOpenAI, Name: "GPT-6",
			Supported: map[string]string{HarnessCodex: "gpt-6"}, Preferred: HarnessCodex,
		}},
	}
	got := AllModels(override)
	if _, ok := ModelByID(got, "openai/gpt-5.6-sol"); ok {
		t.Error("builtin openai/gpt-5.6-sol should be replaced by the override")
	}
	if _, ok := ModelByID(got, "openai/gpt-6"); !ok {
		t.Error("override openai/gpt-6 missing")
	}
	if _, ok := ModelByID(got, "anthropic/claude-sonnet-5"); !ok {
		t.Error("non-overridden anthropic models should remain")
	}
}
