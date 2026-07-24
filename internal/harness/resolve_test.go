// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package harness

import (
	"strings"
	"testing"

	"github.com/bitwise-media-group/patchy/internal/model"
)

func mdl(t *testing.T, id string) model.Model {
	t.Helper()
	m, ok := model.ModelByID(model.Builtins(), id)
	if !ok {
		t.Fatalf("model %q missing from registry", id)
	}
	return m
}

func TestResolveModel(t *testing.T) {
	sonnet := mdl(t, "anthropic/claude-sonnet-5")
	codex := mdl(t, "openai/gpt-5.3-codex")

	tests := []struct {
		name        string
		m           model.Model
		enabled     []string
		wantHarness string
		wantCLI     string
		wantOK      bool
	}{
		{"preferred enabled", sonnet, []string{"claude", "codex"}, "claude", "claude-sonnet-5", true},
		{"codex model to codex", codex, []string{"claude", "codex"}, "codex", "gpt-5.3-codex", true},
		{"preferred disabled, no other supporter", sonnet, []string{"codex"}, "", "", false},
		{"fake fallback runs anything", codex, []string{"fake"}, "fake", "gpt-5.3-codex", true},
		{"fake does not preempt a real harness", sonnet, []string{"claude", "fake"}, "claude", "claude-sonnet-5", true},
		{"nothing enabled", sonnet, nil, "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, cli, ok := ResolveModel(tt.m, EnabledSet(tt.enabled))
			if h != tt.wantHarness || cli != tt.wantCLI || ok != tt.wantOK {
				t.Errorf("ResolveModel = (%q, %q, %v), want (%q, %q, %v)",
					h, cli, ok, tt.wantHarness, tt.wantCLI, tt.wantOK)
			}
		})
	}
}

func TestValidateAllowlist(t *testing.T) {
	models := model.Builtins()

	// Fully covered by the enabled set.
	if err := ValidateAllowlist(models,
		[]string{"anthropic/claude-sonnet-5", "openai/gpt-5.3-codex"},
		EnabledSet([]string{"claude", "codex"})); err != nil {
		t.Errorf("covered allowlist: unexpected error %v", err)
	}

	// An openai model with only claude enabled is uncovered.
	err := ValidateAllowlist(models,
		[]string{"anthropic/claude-sonnet-5", "openai/gpt-5.3-codex"},
		EnabledSet([]string{"claude"}))
	if err == nil || !strings.Contains(err.Error(), "openai/gpt-5.3-codex") {
		t.Errorf("uncovered allowlist: err = %v, want it to name openai/gpt-5.3-codex", err)
	}

	// An unknown model id is rejected.
	err = ValidateAllowlist(models, []string{"anthropic/made-up"}, EnabledSet([]string{"claude"}))
	if err == nil || !strings.Contains(err.Error(), "unknown model") {
		t.Errorf("unknown model: err = %v, want an unknown-model error", err)
	}
}
