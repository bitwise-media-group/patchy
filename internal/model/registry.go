// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package model

import "slices"

// Provider ids.
const (
	ProviderAnthropic = "anthropic"
	ProviderOpenAI    = "openai"
)

// Harness ids, referenced by the Supported maps below. The harness package
// owns the Harness implementations; these constants are the shared vocabulary
// so the registry can name executors without importing that package.
const (
	HarnessClaude = "claude"
	HarnessCodex  = "codex"
	HarnessFake   = "fake"
)

// Providers returns the model vendors in display order.
func Providers() []Provider {
	return []Provider{
		{ID: ProviderAnthropic, Name: "Anthropic"},
		{ID: ProviderOpenAI, Name: "OpenAI"},
	}
}

// builtins returns the canonical model registry: one entry per vendor model,
// each declaring which harnesses can drive it and the CLI-specific id each
// harness's --model flag expects. patchy's harnesses are vendor-native (Claude
// Code drives Anthropic models, Codex drives OpenAI models), so each model
// lists a single supported harness; the map shape leaves room for a harness
// that drives another vendor's models under a diverging id.
func builtins() []Model {
	return []Model{
		// Anthropic — driven by Claude Code.
		{
			ID: "anthropic/claude-haiku-4-5", ProviderID: ProviderAnthropic, Name: "Claude Haiku 4.5",
			InputUSD: usd(1.00), CachedInputUSD: usd(0.10), OutputUSD: usd(5.00),
			Supported: map[string]string{HarnessClaude: "claude-haiku-4-5"},
			Preferred: HarnessClaude,
		},
		{
			ID: "anthropic/claude-sonnet-5", ProviderID: ProviderAnthropic, Name: "Claude Sonnet 5",
			InputUSD: usd(3.00), CachedInputUSD: usd(0.30), OutputUSD: usd(15.00),
			Supported: map[string]string{HarnessClaude: "claude-sonnet-5"},
			Preferred: HarnessClaude,
		},
		{
			ID: "anthropic/claude-opus-5", ProviderID: ProviderAnthropic, Name: "Claude Opus 5",
			InputUSD: usd(5.00), CachedInputUSD: usd(0.50), OutputUSD: usd(25.00),
			Supported: map[string]string{HarnessClaude: "claude-opus-5"},
			Preferred: HarnessClaude,
		},
		{
			ID: "anthropic/claude-fable-5", ProviderID: ProviderAnthropic, Name: "Claude Fable 5",
			InputUSD: usd(10.00), CachedInputUSD: usd(1.00), OutputUSD: usd(50.00),
			Supported: map[string]string{HarnessClaude: "claude-fable-5"},
			Preferred: HarnessClaude,
		},

		// OpenAI — driven by Codex. The codex CLI reports token counts but no
		// cost of its own, so unlike the Anthropic rates (a cross-check on a
		// figure the harness already reports) these are the only thing that
		// prices a codex run at all — see agentresult.stageCost. The GPT-5.6
		// family is three named tiers rather than a size suffix: Luna is the
		// nano-like tier, Terra the mini-like one, Sol the flagship.
		{
			ID: "openai/gpt-5.3-codex", ProviderID: ProviderOpenAI, Name: "GPT-5.3 Codex",
			InputUSD: usd(1.75), CachedInputUSD: usd(0.175), OutputUSD: usd(14.00),
			Supported: map[string]string{HarnessCodex: "gpt-5.3-codex"},
			Preferred: HarnessCodex,
		},
		{
			ID: "openai/gpt-5.6-luna", ProviderID: ProviderOpenAI, Name: "GPT-5.6 Luna",
			InputUSD: usd(1.00), CachedInputUSD: usd(0.10), OutputUSD: usd(6.00),
			Supported: map[string]string{HarnessCodex: "gpt-5.6-luna"},
			Preferred: HarnessCodex,
		},
		{
			ID: "openai/gpt-5.6-terra", ProviderID: ProviderOpenAI, Name: "GPT-5.6 Terra",
			InputUSD: usd(2.50), CachedInputUSD: usd(0.25), OutputUSD: usd(15.00),
			Supported: map[string]string{HarnessCodex: "gpt-5.6-terra"},
			Preferred: HarnessCodex,
		},
		{
			ID: "openai/gpt-5.6-sol", ProviderID: ProviderOpenAI, Name: "GPT-5.6 Sol",
			InputUSD: usd(5.00), CachedInputUSD: usd(0.50), OutputUSD: usd(30.00),
			Supported: map[string]string{HarnessCodex: "gpt-5.6-sol"},
			Preferred: HarnessCodex,
		},
	}
}

// AllModels returns the canonical model registry with any per-provider config
// override applied. overrides maps a provider id to a replacement model list
// (replace, not merge — partial merges create "which price won?" ambiguity);
// only models whose ProviderID is overridden are replaced.
func AllModels(overrides map[string][]Model) []Model {
	if len(overrides) == 0 {
		return builtins()
	}
	var out []Model
	for _, m := range builtins() {
		if _, ok := overrides[m.ProviderID]; ok {
			continue // replaced below
		}
		out = append(out, m)
	}
	for _, p := range Providers() {
		if models, ok := overrides[p.ID]; ok {
			out = append(out, models...)
		}
	}
	return out
}

// Builtins returns the canonical model registry (no overrides). It is the
// default set patchy resolves models against.
func Builtins() []Model { return builtins() }

// ModelByID returns the model with the given canonical id from models, if any.
func ModelByID(models []Model, id string) (Model, bool) {
	for _, m := range models {
		if m.ID == id {
			return m, true
		}
	}
	return Model{}, false
}

// ProviderByID returns the vendor with the given id, if any.
func ProviderByID(id string) (Provider, bool) {
	for _, p := range Providers() {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

// IsProviderID reports whether id names a known vendor.
func IsProviderID(id string) bool {
	for _, p := range Providers() {
		if p.ID == id {
			return true
		}
	}
	return false
}

// KnownHarnessIDs is the set of harness ids the registry may name in a
// Supported map, in registry order — the deterministic tiebreak order for
// harness resolution when a model's preferred harness is not enabled.
var KnownHarnessIDs = []string{HarnessClaude, HarnessCodex, HarnessFake}

// IsKnownHarnessID reports whether id names a harness the registry knows.
func IsKnownHarnessID(id string) bool { return slices.Contains(KnownHarnessIDs, id) }
