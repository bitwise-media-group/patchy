// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package runnercfg

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/bitwise-media-group/patchy/internal/cli"
	"github.com/bitwise-media-group/patchy/internal/jobs"
	"github.com/bitwise-media-group/patchy/internal/model"
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
			name:    "claude defaults to the api key",
			args:    []string{"--claude-agent-image", "claude:1"},
			wantEnv: map[string]string{"claude": "ANTHROPIC_API_KEY"},
		},
		{
			name: "claude accepts the oauth token",
			args: []string{"--claude-agent-image", "claude:1",
				"--claude-secret-env", "CLAUDE_CODE_OAUTH_TOKEN"},
			wantEnv: map[string]string{"claude": "CLAUDE_CODE_OAUTH_TOKEN"},
		},
		{
			name: "claude accepts the auth token",
			args: []string{"--claude-agent-image", "claude:1",
				"--claude-secret-env", "ANTHROPIC_AUTH_TOKEN"},
			wantEnv: map[string]string{"claude": "ANTHROPIC_AUTH_TOKEN"},
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
			name:    "copilot defaults to the copilot github token",
			args:    []string{"--copilot-agent-image", "copilot:1"},
			wantEnv: map[string]string{"copilot": "COPILOT_GITHUB_TOKEN"},
		},
		{
			// copilot authenticates with a GitHub token, so the gh CLI's own
			// variable names are legitimate channels for it — and only for it.
			name: "copilot accepts the gh token",
			args: []string{"--copilot-agent-image", "copilot:1",
				"--copilot-secret-env", "GH_TOKEN"},
			wantEnv: map[string]string{"copilot": "GH_TOKEN"},
		},
		{
			name: "copilot accepts the github token",
			args: []string{"--copilot-agent-image", "copilot:1",
				"--copilot-secret-env", "GITHUB_TOKEN"},
			wantEnv: map[string]string{"copilot": "GITHUB_TOKEN"},
		},
		{
			name: "copilot rejects a model api key",
			args: []string{"--copilot-agent-image", "copilot:1",
				"--copilot-secret-env", "ANTHROPIC_API_KEY"},
			wantErrs: []string{"--copilot-secret-env", "ANTHROPIC_API_KEY", "COPILOT_GITHUB_TOKEN"},
		},
		{
			// A GitHub token is copilot's channel and nobody else's; claude must
			// not accept one just because it is a real credential variable.
			name: "claude rejects a copilot channel",
			args: []string{"--claude-agent-image", "claude:1",
				"--claude-secret-env", "COPILOT_GITHUB_TOKEN"},
			wantErrs: []string{"--claude-secret-env", "COPILOT_GITHUB_TOKEN", "ANTHROPIC_API_KEY"},
		},
		{
			// A real credential variable, but the other vendor's: accepted
			// nowhere but the harness that reads it.
			name: "claude rejects a codex channel",
			args: []string{"--claude-agent-image", "claude:1",
				"--claude-secret-env", "CODEX_ACCESS_TOKEN"},
			wantErrs: []string{"--claude-secret-env", "CODEX_ACCESS_TOKEN", "ANTHROPIC_API_KEY"},
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
// the env var, so a validated channel is only half the credential.
func TestRunnersCredentialWiring(t *testing.T) {
	runners, err := Runners(newOpts(t,
		"--claude-agent-image", "claude:1",
		"--codex-agent-image", "codex:1",
		"--codex-secret", "chatgpt-workspace",
		"--codex-secret-key", "token",
		"--codex-secret-env", "CODEX_ACCESS_TOKEN",
	))
	if err != nil {
		t.Fatalf("Runners: %v", err)
	}

	// Defaults survive when only the codex side is overridden.
	claude := runners["claude"]
	if claude.Image != "claude:1" || claude.Secret != "patchy-anthropic" || claude.SecretKey != "api-key" {
		t.Errorf("claude runner = %+v, want the defaulted anthropic credential", claude)
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

// TestRunnersCopilotCredentialDefaults pins the copilot runner's defaults. They
// deliberately differ from the vendor-native runners: the Secret is named for
// the harness rather than a vendor because the credential is a GitHub token,
// and its key is "token" for the same reason.
func TestRunnersCopilotCredentialDefaults(t *testing.T) {
	runners, err := Runners(newOpts(t, "--copilot-agent-image", "copilot:1"))
	if err != nil {
		t.Fatalf("Runners: %v", err)
	}
	copilot := runners["copilot"]
	want := jobs.Runner{
		Image:     "copilot:1",
		Secret:    "patchy-copilot",
		SecretKey: "token",
		SecretEnv: "COPILOT_GITHUB_TOKEN",
	}
	if copilot != want {
		t.Errorf("copilot runner = %+v, want %+v", copilot, want)
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

	for _, tt := range []struct{ flag, harness string }{
		{"claude-secret-env", model.HarnessClaude},
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
