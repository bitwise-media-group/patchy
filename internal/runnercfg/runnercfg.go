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
// its credential Secret name/key/env, and the harness restrict list. Mutually
// exclusive with RegisterEvolveFlags within one binary — both register the
// shared credential flags.
func RegisterFlags(f *pflag.FlagSet) {
	f.String("claude-agent-image", "", "claude-agent-runner image (claude CLI); unset disables the claude runner")
	f.String("codex-agent-image", "", "codex-agent-runner image (codex CLI); unset disables the codex runner")
	f.String("copilot-agent-image", "",
		"copilot-agent-runner image (copilot CLI); unset disables the copilot runner")
	f.String("fake-agent-image", "", "fake agent image for dev/e2e (replays fixtures, no credential)")

	registerSharedFlags(f)
}

// RegisterEvolveFlags adds the evolve-runner fleet flags for the evaluation
// controller: an evolve-runner image per harness plus the same shared
// credential Secret flags the finding runners use — one Secret per model
// vendor in the agents namespace, whichever fleet exercises it.
func RegisterEvolveFlags(f *pflag.FlagSet) {
	f.String("evolve-claude-image", "",
		"evolve-runner image with the claude CLI; unset disables the claude evaluation runner")
	f.String("evolve-codex-image", "",
		"evolve-runner image with the codex CLI; unset disables the codex evaluation runner")
	f.String("evolve-copilot-image", "",
		"evolve-runner image with the copilot CLI; unset disables the copilot evaluation runner")
	f.String("evolve-fake-image", "", "fake evolve runner for dev/e2e (replays fixtures, no credential)")

	registerSharedFlags(f)
}

// registerSharedFlags adds the credential and restrict flags common to both
// runner fleets.
func registerSharedFlags(f *pflag.FlagSet) {
	f.String("claude-secret", "patchy-anthropic", "Secret (agent namespace) holding the Anthropic credential")
	f.String("claude-secret-key", "api-key", "key within the Anthropic credential Secret")
	f.String("claude-secret-env", "ANTHROPIC_API_KEY", secretEnvUsage("Anthropic", model.HarnessClaude))

	f.String("codex-secret", "patchy-openai", "Secret (agent namespace) holding the OpenAI credential")
	f.String("codex-secret-key", "api-key", "key within the OpenAI credential Secret")
	f.String("codex-secret-env", "OPENAI_API_KEY", secretEnvUsage("OpenAI", model.HarnessCodex))

	// Copilot's credential is a GitHub token rather than a model API key, so
	// the default Secret is named for the harness, not a vendor.
	f.String("copilot-secret", "patchy-copilot", "Secret (agent namespace) holding the GitHub Copilot credential")
	f.String("copilot-secret-key", "token", "key within the GitHub Copilot credential Secret")
	f.String("copilot-secret-env", "COPILOT_GITHUB_TOKEN", secretEnvUsage("GitHub Copilot", model.HarnessCopilot))

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
	if img := opts.String("copilot-agent-image"); img != "" {
		env := opts.String("copilot-secret-env")
		if !accepts(model.HarnessCopilot, env) {
			return nil, fmt.Errorf("--copilot-secret-env %q is not a credential the copilot harness accepts (one of %v)",
				env, envKeys(model.HarnessCopilot))
		}
		runners[model.HarnessCopilot] = jobs.Runner{
			Image: img, Secret: opts.String("copilot-secret"),
			SecretKey: opts.String("copilot-secret-key"), SecretEnv: env,
		}
	}
	if img := opts.String("fake-agent-image"); img != "" {
		runners[model.HarnessFake] = jobs.Runner{Image: img} // no credential
	}

	if len(runners) == 0 {
		return nil, errors.New("no agent runner configured (set at least one of " +
			"--claude-agent-image / --codex-agent-image / --copilot-agent-image / --fake-agent-image)")
	}
	return runners, nil
}

// EvolveRunners builds the evolve-runner fleet from the flags, mirroring
// Runners: a harness is a candidate only when its evolve image flag is set,
// and its credential env var is validated against the harness's accepted
// channels. Credentials reuse the shared --<harness>-secret flags.
func EvolveRunners(opts *cli.Options) (map[string]jobs.Runner, error) {
	runners := map[string]jobs.Runner{}

	// Flag names derive from the harness ids: evolve-<id>-image and
	// <id>-secret{,-key,-env}.
	for _, h := range []string{model.HarnessClaude, model.HarnessCodex, model.HarnessCopilot} {
		img := opts.String("evolve-" + h + "-image")
		if img == "" {
			continue
		}
		env := opts.String(h + "-secret-env")
		if !accepts(h, env) {
			return nil, fmt.Errorf("--%s-secret-env %q is not a credential the %s harness accepts (one of %v)",
				h, env, h, envKeys(h))
		}
		runners[h] = jobs.Runner{
			Image: img, Secret: opts.String(h + "-secret"),
			SecretKey: opts.String(h + "-secret-key"), SecretEnv: env,
		}
	}
	if img := opts.String("evolve-fake-image"); img != "" {
		runners[model.HarnessFake] = jobs.Runner{Image: img} // no credential
	}

	if len(runners) == 0 {
		return nil, errors.New("no evolve runner configured (set at least one of " +
			"--evolve-claude-image / --evolve-codex-image / --evolve-copilot-image / --evolve-fake-image)")
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

// secretEnvUsage renders a --<harness>-secret-env help string from the
// harness's own accepted channels, so the flag can never advertise a set that
// the startup validation below disagrees with — the two drifted apart once
// already, leaving the claude flag naming two of its three channels.
func secretEnvUsage(vendor, harnessID string) string {
	return fmt.Sprintf("env var the %s credential is injected as (one of %s)",
		vendor, strings.Join(envKeys(harnessID), ", "))
}

func accepts(harnessID, env string) bool { return slices.Contains(envKeys(harnessID), env) }

func envKeys(harnessID string) []string {
	if h, ok := harness.ByID(harnessID); ok {
		return h.EnvKeys()
	}
	return nil
}
