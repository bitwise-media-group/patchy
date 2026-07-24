// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package model

import (
	"math"
	"slices"
	"strings"
)

// Provider is a model vendor: the entity that owns and prices a family of
// models (Anthropic, OpenAI). It is distinct from a harness — the CLI that
// drives a model — because several harnesses can in principle run one vendor's
// model, and the vendor id keys pricing while the harness id keys execution.
type Provider struct {
	ID   string `json:"id"`   // registry key, e.g. "anthropic"
	Name string `json:"name"` // human name, e.g. "Anthropic"
}

// Model is one canonical, vendor-owned model. ID is provider-qualified
// ("anthropic/claude-sonnet-5") and is the stable id patchy stores in config,
// the investigation report, and the CRDs; the executing harness never appears
// in it.
//
// Supported maps each harness id that can run this model to the CLI-specific
// model-id string that harness's --model flag expects — this is where any
// harness/vendor id divergence lives. Preferred is the harness chosen when
// several supported harnesses are enabled; it is always a key of Supported.
//
// InputUSD/OutputUSD are USD per 1M tokens (standard tier, cache-miss rates);
// nil means the vendor has not published per-token pricing. They exist so a
// harness that reports token usage but not cost (codex) can still be priced.
type Model struct {
	ID         string            `json:"id"`
	ProviderID string            `json:"provider_id"`
	Name       string            `json:"name"`
	InputUSD   *float64          `json:"input_per_mtok"`
	OutputUSD  *float64          `json:"output_per_mtok"`
	Supported  map[string]string `json:"supported"`
	Preferred  string            `json:"preferred"`
}

// Key is the canonical, provider-qualified id ("anthropic/claude-sonnet-5").
func (m Model) Key() string { return m.ID }

// BareID is the id without its provider prefix ("claude-sonnet-5") — the
// vendor's own model id, independent of the executing harness.
func (m Model) BareID() string {
	if _, id, ok := strings.Cut(m.ID, "/"); ok {
		return id
	}
	return m.ID
}

// CLIModelID returns the harness-specific model-id string this model's --model
// flag expects for harnessID, and whether the harness supports it.
func (m Model) CLIModelID(harnessID string) (string, bool) {
	id, ok := m.Supported[harnessID]
	return id, ok
}

// Supports reports whether harnessID can run this model.
func (m Model) Supports(harnessID string) bool {
	_, ok := m.Supported[harnessID]
	return ok
}

// SupportedHarnessIDs lists the harness ids that can run this model, sorted for
// stable display.
func (m Model) SupportedHarnessIDs() []string {
	ids := make([]string, 0, len(m.Supported))
	for id := range m.Supported {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// UsageCostUSD prices a measured token usage at the model's rates, or nil when
// the model has no published pricing. It is the fallback for a harness that
// reports no cost of its own: every input-side count (fresh input, cache reads,
// and cache writes) is priced at the input rate so the figure still reflects
// the whole session.
func UsageCostUSD(m Model, inputTokens, cacheReadTokens, cacheCreationTokens, outputTokens int) *float64 {
	if m.InputUSD == nil || m.OutputUSD == nil {
		return nil
	}
	var cost float64
	for _, in := range []int{inputTokens, cacheReadTokens, cacheCreationTokens} {
		cost += float64(in) / 1e6 * *m.InputUSD
	}
	cost += float64(outputTokens) / 1e6 * *m.OutputUSD
	cost = round6(cost)
	return &cost
}

func round6(x float64) float64 { return math.Round(x*1e6) / 1e6 }

func usd(x float64) *float64 { return new(x) }
