// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package runnercfg

import (
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/bitwise-media-group/patchy/internal/cli"
	"github.com/bitwise-media-group/patchy/internal/model"
	"github.com/bitwise-media-group/patchy/internal/provider"
)

// newOpts builds the flag surface a controller binary presents and resolves
// args through it, so these tests exercise the real flag/viper path Runners
// reads rather than a hand-built value.
func newOpts(t *testing.T, args ...string) *cli.Options {
	t.Helper()
	o := cli.NewOptions()
	cmd := &cobra.Command{Use: "test", RunE: func(*cobra.Command, []string) error { return nil }}
	o.Bind(cmd)
	RegisterFlags(cmd.Flags())
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if err := o.Load(cmd); err != nil {
		t.Fatalf("Load: %v", err)
	}
	return o
}

// TestRunnersBrokered covers the startup gate on runner configuration: every
// model-calling runner is brokered, so it configures only with a broker URL
// and a provider its harness can front, carries no credential channel of any
// kind, and tolerates the legacy --<harness>-secret* flags (ignored, with a
// migration notice) rather than failing on them.
func TestRunnersBrokered(t *testing.T) {
	const broker = "http://broker:8080"
	tests := []struct {
		name     string
		args     []string
		want     []string // configured runner ids
		wantErrs []string // substrings; non-empty means Runners must fail
	}{
		{
			name: "claude is brokered",
			args: []string{"--claude-agent-image", "claude:1", "--broker-url", broker},
			want: []string{"claude"},
		},
		{
			name:     "claude without a broker url is not a configuration",
			args:     []string{"--claude-agent-image", "claude:1"},
			wantErrs: []string{"--broker-url", "claude"},
		},
		{
			name: "codex is brokered",
			args: []string{"--codex-agent-image", "codex:1", "--broker-url", broker},
			want: []string{"codex"},
		},
		{
			name:     "codex without a broker url is not a configuration",
			args:     []string{"--codex-agent-image", "codex:1"},
			wantErrs: []string{"--broker-url", "codex"},
		},
		{
			name: "codex rejects a claude provider",
			args: []string{"--codex-agent-image", "codex:1", "--broker-url", broker,
				"--codex-provider", "bedrock"},
			wantErrs: []string{"codex provider", "openai", "azure"},
		},
		{
			name: "claude rejects a codex provider",
			args: []string{"--claude-agent-image", "claude:1", "--broker-url", broker,
				"--claude-provider", "openai"},
			wantErrs: []string{"claude provider", "anthropic"},
		},
		{
			name: "codex rejects a credential in provider env",
			args: []string{"--codex-agent-image", "codex:1", "--broker-url", broker,
				"--codex-provider-env", "OPENAI_API_KEY=sk"},
			wantErrs: []string{"OPENAI_API_KEY", "cannot be set here"},
		},
		{
			// Stale secret flags survive startup: ignored, never validated.
			name: "stale secret flags are tolerated",
			args: []string{"--claude-agent-image", "claude:1", "--codex-agent-image", "codex:1",
				"--broker-url", broker,
				"--claude-secret-env", "OPENAI_API_KEY", "--codex-secret-env", "CODEX_TOKEN"},
			want: []string{"claude", "codex"},
		},
		{
			name: "fake runner needs no broker",
			args: []string{"--fake-agent-image", "fake:1"},
			want: []string{"fake"},
		},
		{
			name:     "no image configures no runner",
			args:     nil,
			wantErrs: []string{"no agent runner configured"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runners, err := Runners(newOpts(t, tt.args...))

			if len(tt.wantErrs) > 0 {
				if err == nil {
					t.Fatalf("Runners = %+v, want an error", runners)
				}
				for _, want := range tt.wantErrs {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error %q does not mention %q", err, want)
					}
				}
				return
			}

			if err != nil {
				t.Fatalf("Runners: %v", err)
			}
			if got := keys(runners); !slices.Equal(got, tt.want) {
				t.Errorf("configured runners = %v, want %v", got, tt.want)
			}
			for id, r := range runners {
				if r.Secret != "" || r.SecretEnv != "" {
					t.Errorf("%s runner carries a Secret channel: %+v", id, r)
				}
				if brokered := id != model.HarnessFake; r.Brokered != brokered {
					t.Errorf("%s Brokered = %v, want %v", id, r.Brokered, brokered)
				}
			}
		})
	}
}

// TestRunnersGatewayEnv: each brokered runner's gateway env points its CLI at
// its own broker route — claude through the base-URL override, codex through
// the rendered config.toml's custom provider.
func TestRunnersGatewayEnv(t *testing.T) {
	runners, err := Runners(newOpts(t,
		"--claude-agent-image", "claude:1",
		"--codex-agent-image", "codex:1",
		"--broker-url", "http://broker:8080",
		"--codex-provider", "azure",
		"--codex-model-map", "openai/gpt-5.3-codex=codex-deploy",
	))
	if err != nil {
		t.Fatalf("Runners: %v", err)
	}

	claude := runners["claude"]
	if got := claude.Env["ANTHROPIC_BASE_URL"]; got != "http://broker:8080/anthropic" {
		t.Errorf("claude ANTHROPIC_BASE_URL = %q, want the broker's anthropic route", got)
	}
	if _, ok := claude.Env[provider.CodexConfigEnv]; ok {
		t.Errorf("claude runner carries a codex config")
	}

	codex := runners["codex"]
	cfg := codex.Env[provider.CodexConfigEnv]
	if !strings.Contains(cfg, `base_url = "http://broker:8080/azure/v1"`) {
		t.Errorf("codex config does not point at the broker's azure route:\n%s", cfg)
	}
	if got := codex.Env["PATCHY_MODEL_MAP"]; got != "openai/gpt-5.3-codex=codex-deploy" {
		t.Errorf("codex PATCHY_MODEL_MAP = %q, want the azure deployment map", got)
	}
	if _, ok := codex.Env["ANTHROPIC_BASE_URL"]; ok {
		t.Errorf("codex runner carries claude gateway env")
	}
}

// TestLegacySecretFlagsIgnored: the pre-broker credential flags stay
// registered (a deployment still passing them must start) and say they are
// ignored.
func TestLegacySecretFlagsIgnored(t *testing.T) {
	f := pflag.NewFlagSet("test", pflag.ContinueOnError)
	RegisterFlags(f)
	for _, s := range legacySecretFlags {
		flag := f.Lookup(s.flag)
		if flag == nil {
			t.Errorf("--%s is not registered", s.flag)
			continue
		}
		if !strings.HasPrefix(flag.Usage, "IGNORED") {
			t.Errorf("--%s usage %q does not say it is ignored", s.flag, flag.Usage)
		}
	}
}

// TestResolveAzureCoverage: like foundry, azure deployment names cannot be
// derived, so an OpenAI model codex would run without a map entry fails at
// startup — while claude's models are not the codex runner's problem.
func TestResolveAzureCoverage(t *testing.T) {
	opts := newOpts(t,
		"--claude-agent-image", "claude:1",
		"--codex-agent-image", "codex:1",
		"--broker-url", "http://127.0.0.1:1",
		"--codex-provider", "azure",
		"--codex-model-map", "openai/gpt-5.3-codex=codex-deploy",
	)
	runners, err := Runners(opts)
	if err != nil {
		t.Fatalf("Runners: %v", err)
	}
	ctx, cs := t.Context(), fake.NewClientset()
	_, err = Resolve(ctx, opts, cs, "patchy-agents", runners, nil,
		[]string{"openai/gpt-5.3-codex", "openai/gpt-5.6-sol"}, "anthropic/claude-sonnet")
	if err == nil || !strings.Contains(err.Error(), "openai/gpt-5.6-sol") {
		t.Errorf("Resolve error = %v, want the uncovered model named", err)
	}
	enabled, err := Resolve(ctx, opts, cs, "patchy-agents", runners, nil,
		[]string{"openai/gpt-5.3-codex", "anthropic/claude-sonnet"}, "anthropic/claude-sonnet")
	if err != nil {
		t.Fatalf("Resolve with full coverage: %v", err)
	}
	if !slices.Equal(enabled, []string{"claude", "codex"}) {
		t.Errorf("enabled = %v, want claude and codex (brokered, no Secret probe)", enabled)
	}
}

// TestResolveFoundryCoverage: foundry has no derivable model ids, so a model
// the allowlist (or a stage default) lets claude run without a map entry must
// fail at startup — the alternative is a mid-run failure with a deployment
// name Foundry never heard of.
func TestResolveFoundryCoverage(t *testing.T) {
	args := []string{
		"--claude-agent-image", "claude:1",
		// An unroutable broker URL: the readiness probe must be non-fatal.
		"--broker-url", "http://127.0.0.1:1",
		"--claude-provider", "foundry",
		"--claude-model-map", "anthropic/claude-sonnet-5=sonnet-deploy",
	}
	opts := newOpts(t, args...)
	runners, err := Runners(opts)
	if err != nil {
		t.Fatalf("Runners: %v", err)
	}

	ctx := t.Context()
	cs := fake.NewClientset()
	_, err = Resolve(ctx, opts, cs, "patchy-agents", runners, nil,
		[]string{"anthropic/claude-sonnet-5", "anthropic/claude-opus-5"}, "anthropic/claude-sonnet-5")
	if err == nil || !strings.Contains(err.Error(), "anthropic/claude-opus-5") {
		t.Errorf("Resolve error = %v, want the uncovered model named", err)
	}

	enabled, err := Resolve(ctx, opts, cs, "patchy-agents", runners, nil,
		[]string{"anthropic/claude-sonnet-5"}, "anthropic/claude-sonnet-5")
	if err != nil {
		t.Fatalf("Resolve with full coverage: %v", err)
	}
	// The brokered runner is enabled with no credential Secret in the agent
	// namespace — enablement is configuration, the broker holds the key.
	if !slices.Contains(enabled, "claude") {
		t.Errorf("enabled = %v, want claude (brokered, no Secret probe)", enabled)
	}
}

// TestCanonicalModels: aliases validate and come back concrete, duplicates
// collapse, an unregistered-but-well-formed version is kept (and warned
// about), and a bad alias fails startup.
func TestCanonicalModels(t *testing.T) {
	var logs strings.Builder
	log := slog.New(slog.NewTextHandler(&logs, nil))

	allow, defs, err := CanonicalModels(log,
		[]string{"anthropic/claude-sonnet", "anthropic/claude-opus", "anthropic/claude-opus-5-5"},
		"anthropic/claude-sonnet", "")
	if err != nil {
		t.Fatalf("CanonicalModels(defaults): %v", err)
	}
	if want := []string{"anthropic/claude-sonnet-5-5", "anthropic/claude-opus-5-5"}; !slices.Equal(allow, want) {
		t.Errorf("allowlist = %v, want %v", allow, want)
	}
	if want := []string{"anthropic/claude-sonnet-5-5", ""}; !slices.Equal(defs, want) {
		t.Errorf("defaults = %v, want %v", defs, want)
	}
	if strings.Contains(logs.String(), "not in the registry") {
		t.Errorf("registered models warned about: %s", logs.String())
	}

	allow, _, err = CanonicalModels(log, []string{"anthropic/claude-opus", "anthropic/claude-opus-6"})
	if err != nil {
		t.Fatalf("CanonicalModels(pattern): %v", err)
	}
	if want := []string{"anthropic/claude-opus-5-5", "anthropic/claude-opus-6"}; !slices.Equal(allow, want) {
		t.Errorf("allowlist = %v, want %v", allow, want)
	}
	if !strings.Contains(logs.String(), "anthropic/claude-opus-6") {
		t.Errorf("no warning naming the unpriced model: %q", logs.String())
	}

	if _, _, err := CanonicalModels(log, []string{"anthropic/claude-nosuch"}); err == nil {
		t.Error("unknown alias family accepted")
	}
	if _, _, err := CanonicalModels(log, nil, "openai/not-a-model"); err == nil {
		t.Error("unknown default accepted")
	}
}

// TestResolveBedrockAliasAndPattern is the startup smoke path: an alias plus
// an unregistered version on Bedrock resolve, validate, and both land in the
// pod's PATCHY_MODEL_MAP with derived inference-profile ids.
func TestResolveBedrockAliasAndPattern(t *testing.T) {
	opts := newOpts(t,
		"--claude-agent-image", "claude:1",
		"--broker-url", "http://127.0.0.1:1",
		"--claude-provider", "bedrock",
		"--claude-provider-region", "us-east-1",
	)
	allow, defs, err := CanonicalModels(nil,
		[]string{"anthropic/claude-opus", "anthropic/claude-opus-6"}, "anthropic/claude-sonnet")
	if err != nil {
		t.Fatal(err)
	}
	runners, err := Runners(opts, append(slices.Clone(allow), defs...)...)
	if err != nil {
		t.Fatalf("Runners: %v", err)
	}
	mm, err := provider.ParseModelMap(runners[model.HarnessClaude].Env["PATCHY_MODEL_MAP"])
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{
		"anthropic/claude-opus-5-5":   "us.anthropic.claude-opus-5-5",
		"anthropic/claude-opus-6":     "us.anthropic.claude-opus-6",
		"anthropic/claude-sonnet-5-5": "us.anthropic.claude-sonnet-5-5",
	} {
		if mm[id] != want {
			t.Errorf("PATCHY_MODEL_MAP[%s] = %q, want %q", id, mm[id], want)
		}
	}
	if _, err := Resolve(t.Context(), opts, fake.NewClientset(), "patchy-agents", runners, nil,
		allow, defs...); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// Raw aliases straight into Resolve validate too.
	if _, err := Resolve(t.Context(), opts, fake.NewClientset(), "patchy-agents", runners, nil,
		[]string{"anthropic/claude-sonnet"}, "anthropic/claude-opus"); err != nil {
		t.Fatalf("Resolve(raw aliases): %v", err)
	}
	if h, cliID, err := ResolveHarness("anthropic/claude-opus-6", []string{"claude", "codex"}); err != nil ||
		h != "claude" || cliID != "claude-opus-6" {
		t.Errorf("ResolveHarness(pattern) = %q, %q, %v; want claude, claude-opus-6", h, cliID, err)
	}
	if _, _, err := ResolveHarness("anthropic/claude-opus-6", []string{"codex"}); err == nil {
		t.Error("pattern model resolved without the claude harness enabled")
	}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
