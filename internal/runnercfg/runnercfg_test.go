// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package runnercfg

import (
	"encoding/json"
	"log/slog"
	"os"
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

// TestRunnersSecretEnvValidation covers the startup gate on the credential
// channel. It is the only thing standing between a typo'd --<harness>-secret-env
// and a controller that runs happily until an agent pod comes up with its
// credential in a variable the CLI never reads. The channels are per-harness,
// so naming the other harness's variable must fail just as a nonsense one does.
func TestRunnersSecretEnvValidation(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantEnv  map[string]string // harness -> resolved SecretEnv
		wantErrs []string          // substrings; non-empty means Runners must fail
	}{
		{
			// Claude is brokered: no credential channel at all, and the runner
			// only configures with a broker URL.
			name:    "claude is brokered with no secret env",
			args:    []string{"--claude-agent-image", "claude:1", "--broker-url", "http://broker:8080"},
			wantEnv: map[string]string{"claude": ""},
		},
		{
			name:     "claude without a broker url is not a configuration",
			args:     []string{"--claude-agent-image", "claude:1"},
			wantErrs: []string{"--broker-url"},
		},
		{
			// A stale --claude-secret-env survives startup: the flag is ignored
			// (with a migration notice), never validated against channels.
			name: "claude tolerates a stale secret env",
			args: []string{"--claude-agent-image", "claude:1", "--broker-url", "http://broker:8080",
				"--claude-secret-env", "OPENAI_API_KEY"},
			wantEnv: map[string]string{"claude": ""},
		},
		{
			name:    "codex defaults to the api key",
			args:    []string{"--codex-agent-image", "codex:1"},
			wantEnv: map[string]string{"codex": "OPENAI_API_KEY"},
		},
		{
			name: "codex accepts the chatgpt-plan access token",
			args: []string{"--codex-agent-image", "codex:1",
				"--codex-secret-env", "CODEX_ACCESS_TOKEN"},
			wantEnv: map[string]string{"codex": "CODEX_ACCESS_TOKEN"},
		},
		{
			name: "codex accepts the codex api key",
			args: []string{"--codex-agent-image", "codex:1",
				"--codex-secret-env", "CODEX_API_KEY"},
			wantEnv: map[string]string{"codex": "CODEX_API_KEY"},
		},
		{
			name: "codex rejects a claude channel",
			args: []string{"--codex-agent-image", "codex:1",
				"--codex-secret-env", "ANTHROPIC_API_KEY"},
			wantErrs: []string{"--codex-secret-env", "ANTHROPIC_API_KEY", "CODEX_ACCESS_TOKEN"},
		},
		{
			// The error must enumerate the accepted channels; a bare rejection
			// leaves the operator guessing at the spelling.
			name: "codex rejects a typo and lists the alternatives",
			args: []string{"--codex-agent-image", "codex:1",
				"--codex-secret-env", "CODEX_TOKEN"},
			wantErrs: []string{"--codex-secret-env", "OPENAI_API_KEY", "CODEX_API_KEY", "CODEX_ACCESS_TOKEN"},
		},
		{
			// The fake runner carries no credential, so there is no channel to
			// validate and a stale --codex-secret-env must not fail startup.
			name:    "fake runner needs no credential",
			args:    []string{"--fake-agent-image", "fake:1", "--codex-secret-env", "CODEX_TOKEN"},
			wantEnv: map[string]string{"fake": ""},
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
			if len(runners) != len(tt.wantEnv) {
				t.Errorf("configured runners = %v, want %v", keys(runners), keys(tt.wantEnv))
			}
			for id, wantEnv := range tt.wantEnv {
				r, ok := runners[id]
				if !ok {
					t.Fatalf("runner %q not configured", id)
				}
				if r.SecretEnv != wantEnv {
					t.Errorf("%s SecretEnv = %q, want %q", id, r.SecretEnv, wantEnv)
				}
			}
		})
	}
}

// TestRunnersCredentialWiring: the Secret name and key ride the same flags as
// the env var, so a validated channel is only half the credential. Claude is
// brokered — no Secret reference of any kind — while codex keeps its channel.
func TestRunnersCredentialWiring(t *testing.T) {
	runners, err := Runners(newOpts(t,
		"--claude-agent-image", "claude:1",
		"--broker-url", "http://broker:8080",
		"--codex-agent-image", "codex:1",
		"--codex-secret", "chatgpt-workspace",
		"--codex-secret-key", "token",
		"--codex-secret-env", "CODEX_ACCESS_TOKEN",
	))
	if err != nil {
		t.Fatalf("Runners: %v", err)
	}

	claude := runners["claude"]
	if !claude.Brokered || claude.Secret != "" || claude.SecretEnv != "" {
		t.Errorf("claude runner = %+v, want brokered with no Secret channel", claude)
	}
	if got := claude.Env["ANTHROPIC_BASE_URL"]; got != "http://broker:8080/anthropic" {
		t.Errorf("claude ANTHROPIC_BASE_URL = %q, want the broker's anthropic route", got)
	}

	codex := runners["codex"]
	want := map[string]string{
		"image":     "codex:1",
		"secret":    "chatgpt-workspace",
		"secretKey": "token",
		"secretEnv": "CODEX_ACCESS_TOKEN",
	}
	got := map[string]string{
		"image":     codex.Image,
		"secret":    codex.Secret,
		"secretKey": codex.SecretKey,
		"secretEnv": codex.SecretEnv,
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("codex %s = %q, want %q", k, got[k], w)
		}
	}
}

// chartSecretEnvEnum is where the Helm chart constrains
// agent.runners.<harness>.secretEnv.
const chartSecretEnvEnum = "../../charts/patchy/values.schema.json"

// TestChartSecretEnvEnumMatchesHarnesses guards the one copy of the credential
// vocabulary that Go cannot reach: helm validates secretEnv against a hardcoded
// enum in values.schema.json, while Runners validates the same field against
// harness.EnvKeys. Nothing connected the two, and they drifted — the enum named
// only OPENAI_API_KEY for codex, so a chart install using the CODEX_API_KEY or
// CODEX_ACCESS_TOKEN channel that Runners accepts failed lint before it ever
// reached a controller. Adding a channel to a harness must add it here.
func TestChartSecretEnvEnumMatchesHarnesses(t *testing.T) {
	raw, err := os.ReadFile(chartSecretEnvEnum)
	if err != nil {
		t.Fatalf("read chart schema: %v", err)
	}
	var doc struct {
		Definitions struct {
			AgentRunner struct {
				Properties struct {
					SecretEnv struct {
						Enum []string `json:"enum"`
					} `json:"secretEnv"`
				} `json:"properties"`
			} `json:"agentRunner"`
		} `json:"definitions"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse chart schema: %v", err)
	}
	got := doc.Definitions.AgentRunner.Properties.SecretEnv.Enum
	if len(got) == 0 {
		t.Fatalf("%s: no secretEnv enum found — did the schema shape change?", chartSecretEnvEnum)
	}

	// Every channel every harness accepts, in registry order. The fake harness
	// carries no credential and contributes none.
	var want []string
	for _, id := range model.KnownHarnessIDs {
		if id == model.HarnessFake {
			continue
		}
		want = append(want, envKeys(id)...)
	}

	inEnum := make(map[string]bool, len(got))
	for _, e := range got {
		inEnum[e] = true
	}
	for _, env := range want {
		if !inEnum[env] {
			t.Errorf("%s: secretEnv enum is missing %q, a channel a harness accepts — "+
				"a chart install using it fails lint even though Runners allows it",
				chartSecretEnvEnum, env)
		}
	}
	// The reverse direction: an enum entry no harness accepts would pass helm
	// lint and then fail at controller startup.
	accepted := make(map[string]bool, len(want))
	for _, env := range want {
		accepted[env] = true
	}
	for _, e := range got {
		if !accepted[e] {
			t.Errorf("%s: secretEnv enum offers %q, which no harness accepts — "+
				"helm would accept a value the controller rejects at startup", chartSecretEnvEnum, e)
		}
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

// TestSecretEnvFlagsMatchAcceptedChannels: the --<harness>-secret-env help is
// generated from the harness's accepted channels, so it cannot advertise a set
// the startup validation rejects, or omit one an operator is allowed to pick.
// The default must itself be an accepted channel, or the flag would fail
// validation when left alone.
func TestSecretEnvFlagsMatchAcceptedChannels(t *testing.T) {
	f := pflag.NewFlagSet("test", pflag.ContinueOnError)
	RegisterFlags(f)

	// claude-secret-env is absent on purpose: the claude runner is brokered,
	// the flag is ignored, and its usage says so instead of naming channels.
	for _, tt := range []struct{ flag, harness string }{
		{"codex-secret-env", model.HarnessCodex},
	} {
		t.Run(tt.flag, func(t *testing.T) {
			flag := f.Lookup(tt.flag)
			if flag == nil {
				t.Fatalf("--%s is not registered", tt.flag)
			}
			for _, env := range envKeys(tt.harness) {
				if !strings.Contains(flag.Usage, env) {
					t.Errorf("usage %q omits accepted channel %q", flag.Usage, env)
				}
			}
			if !accepts(tt.harness, flag.DefValue) {
				t.Errorf("default %q is not an accepted channel %v", flag.DefValue, envKeys(tt.harness))
			}
		})
	}
}
