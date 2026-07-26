// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package agentresult

import (
	"testing"

	"github.com/bitwise-media-group/patchy/internal/envelope"
)

func TestFromStageCost(t *testing.T) {
	// A harness-reported cost is used verbatim.
	st := &envelope.Stage{Model: "anthropic/claude-sonnet-5", Usage: envelope.Usage{CostUSD: 1.25}}
	if got := FromStage(st).Usage.CostUSD; got != "1.250000" {
		t.Errorf("reported cost = %q, want 1.250000", got)
	}

	// No reported cost but a priced model: fall back to token pricing.
	// claude-sonnet-5 is 3/15 per MTok: 1_000_000/1e6*3 + 200_000/1e6*15 = 6.0.
	st = &envelope.Stage{
		Model: "anthropic/claude-sonnet-5",
		Usage: envelope.Usage{InputTokens: 1_000_000, OutputTokens: 200_000},
	}
	if got := FromStage(st).Usage.CostUSD; got != "6.000000" {
		t.Errorf("priced fallback = %q, want 6.000000", got)
	}

	// Codex reports tokens but never a cost, so the fallback is the only thing
	// that prices it. gpt-5.3-codex is 1.75/14 per MTok:
	// 1000/1e6*1.75 + 500/1e6*14 = 0.00175 + 0.007 = 0.00875.
	st = &envelope.Stage{
		Model: "openai/gpt-5.3-codex",
		Usage: envelope.Usage{InputTokens: 1000, OutputTokens: 500},
	}
	if got := FromStage(st).Usage.CostUSD; got != "0.008750" {
		t.Errorf("codex priced fallback = %q, want 0.008750", got)
	}

	// A model outside the registry cannot be priced: cost stays empty.
	st = &envelope.Stage{
		Model: "openai/not-in-the-registry",
		Usage: envelope.Usage{InputTokens: 1000, OutputTokens: 500},
	}
	if got := FromStage(st).Usage.CostUSD; got != "" {
		t.Errorf("unknown model cost = %q, want empty", got)
	}
}
