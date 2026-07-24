// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package runnercfg wires the per-harness agent-runner fleet from controller
// flags: the image and credential Secret for each harness, which harnesses a
// deployment enables, and the startup validation that every allowlisted model
// can actually be run. Both job controllers (investigation, remediation) share
// it so the flag surface and enablement rules never drift apart.
package runnercfg

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/pflag"
	"k8s.io/client-go/kubernetes"

	"github.com/bitwise-media-group/patchy/internal/cli"
	"github.com/bitwise-media-group/patchy/internal/harness"
	"github.com/bitwise-media-group/patchy/internal/jobs"
	"github.com/bitwise-media-group/patchy/internal/model"
)

// RegisterFlags adds the per-harness runner flags shared by both job
// controllers: an image per harness (unset = that runner is not configured),
// its credential Secret name/key/env, and the harness restrict list.
func RegisterFlags(f *pflag.FlagSet) {
	f.String("claude-agent-image", "", "claude-agent-runner image (claude CLI); unset disables the claude runner")
	f.String("codex-agent-image", "", "codex-agent-runner image (codex CLI); unset disables the codex runner")
	f.String("fake-agent-image", "", "fake agent image for dev/e2e (replays fixtures, no credential)")

	f.String("claude-secret", "patchy-anthropic", "Secret (agent namespace) holding the Anthropic credential")
	f.String("claude-secret-key", "api-key", "key within the Anthropic credential Secret")
	f.String("claude-secret-env", "ANTHROPIC_API_KEY",
		"env var the Anthropic credential is injected as (ANTHROPIC_API_KEY or CLAUDE_CODE_OAUTH_TOKEN)")

	f.String("codex-secret", "patchy-openai", "Secret (agent namespace) holding the OpenAI credential")
	f.String("codex-secret-key", "api-key", "key within the OpenAI credential Secret")
	f.String("codex-secret-env", "OPENAI_API_KEY", "env var the OpenAI credential is injected as")

	f.String("harnesses", "",
		"restrict enabled harnesses to this comma list (default: any harness whose credential Secret exists)")
}

// Runners builds the configured runner fleet from the flags. A harness is a
// candidate runner only when its image flag is set; the credential env var is
// validated against the harness's accepted credential channels so a
// typo'd --claude-secret-env fails at startup rather than in the pod.
func Runners(opts *cli.Options) (map[string]jobs.Runner, error) {
	runners := map[string]jobs.Runner{}

	if img := opts.String("claude-agent-image"); img != "" {
		env := opts.String("claude-secret-env")
		if !accepts(model.HarnessClaude, env) {
			return nil, fmt.Errorf("--claude-secret-env %q is not a credential the claude harness accepts (one of %v)",
				env, envKeys(model.HarnessClaude))
		}
		runners[model.HarnessClaude] = jobs.Runner{
			Image: img, Secret: opts.String("claude-secret"),
			SecretKey: opts.String("claude-secret-key"), SecretEnv: env,
		}
	}
	if img := opts.String("codex-agent-image"); img != "" {
		env := opts.String("codex-secret-env")
		if !accepts(model.HarnessCodex, env) {
			return nil, fmt.Errorf("--codex-secret-env %q is not a credential the codex harness accepts (one of %v)",
				env, envKeys(model.HarnessCodex))
		}
		runners[model.HarnessCodex] = jobs.Runner{
			Image: img, Secret: opts.String("codex-secret"),
			SecretKey: opts.String("codex-secret-key"), SecretEnv: env,
		}
	}
	if img := opts.String("fake-agent-image"); img != "" {
		runners[model.HarnessFake] = jobs.Runner{Image: img} // no credential
	}

	if len(runners) == 0 {
		return nil, errors.New("no agent runner configured " +
			"(set at least one of --claude-agent-image / --codex-agent-image / --fake-agent-image)")
	}
	return runners, nil
}

// Restrict parses the --harnesses restrict list; empty means auto-detect.
func Restrict(opts *cli.Options) []string { return SplitList(opts.String("harnesses")) }

// SplitList splits a comma-separated flag value, trimming blanks.
func SplitList(s string) []string {
	var out []string
	for _, tok := range strings.Split(s, ",") {
		if tok = strings.TrimSpace(tok); tok != "" {
			out = append(out, tok)
		}
	}
	return out
}

// Resolve probes the configured runners' credentials, computes the enabled
// harness set, and validates coverage: the allowlist must be fully runnable
// and every requiredModel (the investigate/remediate defaults, canonical ids)
// must resolve to an enabled harness. It returns the sorted enabled harness
// ids.
func Resolve(ctx context.Context, cs kubernetes.Interface, namespace string,
	runners map[string]jobs.Runner, restrict, allowlist []string, requiredModels ...string) ([]string, error) {
	enabled, err := jobs.ResolveRunners(ctx, cs, namespace, runners, restrict)
	if err != nil {
		return nil, err
	}
	if len(enabled) == 0 {
		return nil, errors.New("no harness enabled: no configured runner has its credential Secret in the agent namespace")
	}
	set := harness.EnabledSet(enabled)
	if err := harness.ValidateAllowlist(model.Builtins(), allowlist, set); err != nil {
		return nil, fmt.Errorf("model allowlist: %w", err)
	}
	for _, id := range requiredModels {
		if id == "" {
			continue
		}
		if _, _, err := ResolveHarness(id, enabled); err != nil {
			return nil, err
		}
	}
	return enabled, nil
}

// ResolveHarness resolves a canonical model id to its harness and CLI model-id
// given the enabled set, erroring when the model is unknown or unrunnable.
func ResolveHarness(canonical string, enabled []string) (harnessID, cliModelID string, err error) {
	m, ok := model.ModelByID(model.Builtins(), canonical)
	if !ok {
		return "", "", fmt.Errorf("model %q is not in the model registry", canonical)
	}
	h, cliID, ok := harness.ResolveModel(m, harness.EnabledSet(enabled))
	if !ok {
		return "", "", fmt.Errorf("model %q needs one of harnesses %v enabled, but only %v are",
			canonical, m.SupportedHarnessIDs(), enabled)
	}
	return h, cliID, nil
}

func accepts(harnessID, env string) bool { return slices.Contains(envKeys(harnessID), env) }

func envKeys(harnessID string) []string {
	if h, ok := harness.ByID(harnessID); ok {
		return h.EnvKeys()
	}
	return nil
}
