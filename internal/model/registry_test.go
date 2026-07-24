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
	if m.InputUSD != nil || m.OutputUSD != nil {
		t.Errorf("pricing = %v/%v, want nil/nil (subscription-billed)", m.InputUSD, m.OutputUSD)
	}
}

func TestUsageCostUSD(t *testing.T) {
	m, _ := ModelByID(builtins(), "anthropic/claude-sonnet-5") // 3/15 per MTok
	// input+cacheRead+cacheCreation priced at 3/MTok, output at 15/MTok:
	// (1_000_000 + 500_000 + 200_000)/1e6*3 + 100_000/1e6*15 = 5.1 + 1.5 = 6.6
	got := UsageCostUSD(m, 1_000_000, 500_000, 200_000, 100_000)
	if got == nil || *got != 6.6 {
		t.Errorf("UsageCostUSD = %v, want 6.6", got)
	}
	// A model without published pricing prices to nil.
	codex, _ := ModelByID(builtins(), "openai/gpt-5.3-codex")
	if got := UsageCostUSD(codex, 100, 0, 0, 50); got != nil {
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
	if _, ok := ModelByID(got, "openai/gpt-5.5"); ok {
		t.Error("builtin openai/gpt-5.5 should be replaced by the override")
	}
	if _, ok := ModelByID(got, "openai/gpt-6"); !ok {
		t.Error("override openai/gpt-6 missing")
	}
	if _, ok := ModelByID(got, "anthropic/claude-sonnet-5"); !ok {
		t.Error("non-overridden anthropic models should remain")
	}
}
