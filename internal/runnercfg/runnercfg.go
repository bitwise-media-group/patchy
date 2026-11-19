// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package runnercfg wires the per-harness agent-runner fleet from controller
// flags: the image and brokered provider for each harness, which harnesses a
// deployment enables, and the startup validation that every allowlisted model
// can actually be run. Both job controllers (investigation, remediation) share
// it so the flag surface and enablement rules never drift apart.
package runnercfg

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/spf13/pflag"
	"k8s.io/client-go/kubernetes"

	"github.com/bitwise-media-group/patchy/internal/cli"
	"github.com/bitwise-media-group/patchy/internal/harness"
	"github.com/bitwise-media-group/patchy/internal/jobs"
	"github.com/bitwise-media-group/patchy/internal/model"
	"github.com/bitwise-media-group/patchy/internal/provider"
)

// RegisterFlags adds the per-harness runner flags shared by both job
// controllers: an image per harness (unset = that runner is not configured),
// the broker/provider flags, and the harness restrict list. Mutually
// exclusive with RegisterEvolveFlags within one binary — both register the
// shared flags.
func RegisterFlags(f *pflag.FlagSet) {
	f.String("claude-agent-image", "", "claude-agent-runner image (claude CLI); unset disables the claude runner")
	f.String("codex-agent-image", "", "codex-agent-runner image (codex CLI); unset disables the codex runner")
	f.String("fake-agent-image", "", "fake agent image for dev/e2e (replays fixtures, no credential)")

	registerSharedFlags(f)
}

// RegisterEvolveFlags adds the evolve-runner fleet flags for the evaluation
// controller: an evolve-runner image per harness plus the same shared
// broker/provider flags the finding runners use — one broker route per model
// vendor, whichever fleet exercises it.
func RegisterEvolveFlags(f *pflag.FlagSet) {
	f.String("evolve-claude-image", "",
		"evolve-runner image with the claude CLI; unset disables the claude evaluation runner")
	f.String("evolve-codex-image", "",
		"evolve-runner image with the codex CLI; unset disables the codex evaluation runner")
	f.String("evolve-fake-image", "", "fake evolve runner for dev/e2e (replays fixtures, no credential)")

	registerSharedFlags(f)
}

// registerSharedFlags adds the broker/provider and restrict flags common to
// both runner fleets: every model-calling runner (claude, codex) is brokered.
func registerSharedFlags(f *pflag.FlagSet) {
	f.String("broker-url", "",
		"egress credential broker base URL (required for the claude and codex runners; both run proxy-only)")
	f.String("broker-token-audience", jobs.DefaultBrokerAudience,
		"audience the agent pods' projected broker caller tokens are bound to")
	f.String("claude-provider", provider.Anthropic,
		"model provider behind the broker for claude runs: "+strings.Join(provider.Names, ", "))
	f.String("claude-provider-region", "", "provider region (required for bedrock and vertex)")
	f.String("claude-provider-region-prefix", "",
		"bedrock inference-profile prefix override when it cannot be derived from the region")
	f.String("claude-provider-project-id", "", "GCP project id (required for vertex)")
	f.String("claude-model-map", "",
		"canonical=provider-id model overrides, comma separated (required for foundry: deployment names)")
	f.String("claude-provider-env", "",
		"extra NAME=value agent-pod env for the provider, comma separated (credential names rejected)")

	f.String("codex-provider", provider.OpenAI,
		"model provider behind the broker for codex runs: "+strings.Join(provider.CodexNames, ", "))
	f.String("codex-model-map", "",
		"canonical=provider-id model overrides, comma separated (required for azure: deployment names)")
	f.String("codex-provider-env", "",
		"extra NAME=value agent-pod env for the codex provider, comma separated (credential names rejected)")

	// The --<harness>-secret* flags stay registered for back-compat, but
	// both model-calling runners are brokered now and ignore them (a
	// migration notice is logged when they are customized).
	for _, s := range legacySecretFlags {
		f.String(s.flag, s.def, "IGNORED: the "+s.harness+" model credential lives in the egress broker")
	}

	f.String("harnesses", "",
		"restrict enabled harnesses to this comma list (default: every harness whose runner image is set)")
}

// legacySecretFlags are the pre-broker credential Secret flags, kept
// registered (with their old defaults, so a customized value is detectable)
// only to log a migration notice.
var legacySecretFlags = []struct{ harness, flag, def string }{
	{model.HarnessClaude, "claude-secret", "patchy-anthropic"},
	{model.HarnessClaude, "claude-secret-key", "api-key"},
	{model.HarnessClaude, "claude-secret-env", "ANTHROPIC_API_KEY"},
	{model.HarnessCodex, "codex-secret", "patchy-openai"},
	{model.HarnessCodex, "codex-secret-key", "api-key"},
	{model.HarnessCodex, "codex-secret-env", "OPENAI_API_KEY"},
}

// brokeredHarnesses are the harnesses whose runners go through the egress
// credential broker — every one that calls a model API.
var brokeredHarnesses = []string{model.HarnessClaude, model.HarnessCodex}

// Runners builds the configured runner fleet from the flags. A harness is a
// candidate runner only when its image flag is set. The claude and codex
// runners are always brokered — their model traffic goes through the egress
// credential broker and no credential enters the pod. models are the
// canonical ids the fleet may run (the CanonicalModels output): any
// pattern-tier id among them gets a derived claude model-map entry alongside
// the registered models.
func Runners(opts *cli.Options, models ...string) (map[string]jobs.Runner, error) {
	runners := map[string]jobs.Runner{}

	for _, h := range brokeredHarnesses {
		img := opts.String(h + "-agent-image")
		if img == "" {
			continue
		}
		r, err := brokeredRunner(opts, h, img, models...)
		if err != nil {
			return nil, err
		}
		runners[h] = r
	}
	if img := opts.String("fake-agent-image"); img != "" {
		runners[model.HarnessFake] = jobs.Runner{Image: img} // no credential
	}

	if len(runners) == 0 {
		return nil, errors.New("no agent runner configured (set at least one of " +
			"--claude-agent-image / --codex-agent-image / --fake-agent-image)")
	}
	return runners, nil
}

// EvolveRunners builds the evolve-runner fleet from the flags, mirroring
// Runners: a harness is a candidate only when its evolve image flag is set,
// and the claude and codex evolve runners are brokered like their finding
// siblings.
func EvolveRunners(opts *cli.Options) (map[string]jobs.Runner, error) {
	runners := map[string]jobs.Runner{}

	for _, h := range brokeredHarnesses {
		img := opts.String("evolve-" + h + "-image")
		if img == "" {
			continue
		}
		r, err := brokeredRunner(opts, h, img)
		if err != nil {
			return nil, err
		}
		runners[h] = r
	}
	if img := opts.String("evolve-fake-image"); img != "" {
		runners[model.HarnessFake] = jobs.Runner{Image: img} // no credential
	}

	if len(runners) == 0 {
		return nil, errors.New("no evolve runner configured (set at least one of " +
			"--evolve-claude-image / --evolve-codex-image / --evolve-fake-image)")
	}
	return runners, nil
}

// brokeredRunner builds the brokered runner for a harness, shared by both
// fleets: the provider config from the flags, the derived model map, and the
// gateway env the pod gets. --broker-url is required — a claude or codex
// runner without the broker is not a configuration. The legacy
// --<harness>-secret* flags are ignored; customizing them earns a migration
// notice rather than an error. models adds pattern-tier ids to the registry
// the map is derived over.
func brokeredRunner(opts *cli.Options, harnessID, img string, models ...string) (jobs.Runner, error) {
	pcfg, err := ProviderConfig(opts, harnessID)
	if err != nil {
		return jobs.Runner{}, err
	}
	mm, err := provider.EffectiveModelMap(pcfg, model.WithResolved(model.Builtins(), models...))
	if err != nil {
		return jobs.Runner{}, err
	}
	for _, s := range legacySecretFlags {
		if s.harness == harnessID && opts.String(s.flag) != s.def {
			opts.Log.Warn("--"+harnessID+"-secret* is ignored: "+harnessID+" runs proxy-only and the model "+
				"credential lives in the egress broker; move the Secret to the broker's namespace and drop these flags",
				"flag", s.flag)
			break
		}
	}
	return jobs.Runner{Image: img, Brokered: true, Env: provider.Env(pcfg, mm)}, nil
}

// ProviderConfig builds and validates a brokered harness's provider config
// from the shared flags: --broker-url plus the --<harness>-provider*,
// --<harness>-model-map and --<harness>-provider-env flags (the region and
// project-id flags exist for claude's cloud providers only).
func ProviderConfig(opts *cli.Options, harnessID string) (provider.Config, error) {
	if opts.String("broker-url") == "" {
		return provider.Config{}, fmt.Errorf(
			"--broker-url is required when a %s runner is configured (%s runs proxy-only)", harnessID, harnessID)
	}
	mm, err := parseKVList(opts.String(harnessID + "-model-map"))
	if err != nil {
		return provider.Config{}, fmt.Errorf("--%s-model-map: %w", harnessID, err)
	}
	extra, err := parseKVList(opts.String(harnessID + "-provider-env"))
	if err != nil {
		return provider.Config{}, fmt.Errorf("--%s-provider-env: %w", harnessID, err)
	}
	cfg := provider.Config{
		Harness:   harnessID,
		Name:      opts.String(harnessID + "-provider"),
		BrokerURL: opts.String("broker-url"),
		ModelMap:  mm,
		ExtraEnv:  extra,
	}
	if harnessID == model.HarnessClaude {
		cfg.Region = opts.String("claude-provider-region")
		cfg.RegionPrefix = opts.String("claude-provider-region-prefix")
		cfg.ProjectID = opts.String("claude-provider-project-id")
	}
	if err := cfg.Validate(); err != nil {
		return provider.Config{}, err
	}
	return cfg, nil
}

// parseKVList parses a comma-separated NAME=value flag into a map.
func parseKVList(s string) (map[string]string, error) {
	items := SplitList(s)
	if len(items) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(items))
	for _, item := range items {
		k, v, ok := strings.Cut(item, "=")
		if !ok || k == "" || v == "" {
			return nil, fmt.Errorf("entry %q is not NAME=value", item)
		}
		out[k] = v
	}
	return out, nil
}

// Restrict parses the --harnesses restrict list; empty means auto-detect.
func Restrict(opts *cli.Options) []string { return SplitList(opts.String("harnesses")) }

// SplitList splits a comma-separated flag value, trimming blanks.
func SplitList(s string) []string {
	var out []string
	for tok := range strings.SplitSeq(s, ",") {
		if tok = strings.TrimSpace(tok); tok != "" {
			out = append(out, tok)
		}
	}
	return out
}

// CanonicalModels resolves the configured model ids once, at startup: the
// allowlist and each stage default map to concrete canonical ids (family
// aliases become the newest registered version; see model.Resolve), so only
// concrete ids flow downstream into pod env, CRD status and rollups. The
// allowlist is deduplicated; defaults keep their positions (an empty default
// stays empty). An unknown alias family or a malformed id is an error. Each
// pattern-tier id — a well-formed Anthropic version patchy has not
// registered — is logged once as a warning: it runs on claude only and is
// unpriced until registered.
func CanonicalModels(log *slog.Logger, allowlist []string, defaults ...string) (allow, defs []string, err error) {
	allow, err = model.Canonicalize(allowlist)
	if err != nil {
		return nil, nil, fmt.Errorf("model allowlist: %w", err)
	}
	defs = make([]string, len(defaults))
	for i, id := range defaults {
		if strings.TrimSpace(id) == "" {
			continue
		}
		c, err := model.Canonicalize([]string{id})
		if err != nil {
			return nil, nil, err
		}
		defs[i] = c[0]
	}
	var unpriced []string
	for _, id := range append(slices.Clone(allow), defs...) {
		if m, ok := model.Lookup(id); ok && model.Synthesized(m) && !slices.Contains(unpriced, id) {
			unpriced = append(unpriced, id)
		}
	}
	if len(unpriced) > 0 && log != nil {
		log.Warn("models not in the registry: running them on the claude harness only, unpriced until registered",
			"models", unpriced)
	}
	return allow, defs, nil
}

// Resolve probes the configured runners' credentials, computes the enabled
// harness set, and validates coverage: the allowlist must be fully runnable,
// every requiredModel (the investigate/remediate defaults, canonical ids)
// must resolve to an enabled harness, and each brokered runner's model map
// must cover every model resolving to its harness — a foundry or azure gap
// is a startup error here, not a mid-run failure. It also probes the broker's readiness,
// non-fatally: the broker Deployment may simply not be up yet, and a
// deployment-ordering deadlock would be worse than a warning. Returns the
// sorted enabled harness ids.
func Resolve(ctx context.Context, opts *cli.Options, cs kubernetes.Interface, namespace string,
	runners map[string]jobs.Runner, restrict, allowlist []string, requiredModels ...string) ([]string, error) {
	enabled, err := jobs.ResolveRunners(ctx, cs, namespace, runners, restrict)
	if err != nil {
		return nil, err
	}
	if len(enabled) == 0 {
		return nil, errors.New("no harness enabled: no configured runner has its credential Secret in the agent namespace")
	}
	// Canonicalize again (idempotent on CanonicalModels output) so a caller
	// passing raw aliases still validates and covers the concrete ids.
	allowlist, err = model.Canonicalize(allowlist)
	if err != nil {
		return nil, fmt.Errorf("model allowlist: %w", err)
	}
	requiredModels, err = model.Canonicalize(requiredModels)
	if err != nil {
		return nil, err
	}
	required := append(slices.Clone(allowlist), requiredModels...)
	models := model.WithResolved(model.Builtins(), required...)
	set := harness.EnabledSet(enabled)
	if err := harness.ValidateAllowlist(models, allowlist, set); err != nil {
		return nil, fmt.Errorf("model allowlist: %w", err)
	}
	for _, id := range requiredModels {
		if _, _, err := ResolveHarness(id, enabled); err != nil {
			return nil, err
		}
	}
	var brokerURL string
	for _, h := range brokeredHarnesses {
		if r, ok := runners[h]; !ok || !r.Brokered {
			continue
		}
		pcfg, err := ProviderConfig(opts, h)
		if err != nil {
			return nil, err
		}
		mm, err := provider.EffectiveModelMap(pcfg, models)
		if err != nil {
			return nil, err
		}
		if err := provider.ValidateCoverage(pcfg, models, mm, required); err != nil {
			return nil, err
		}
		brokerURL = pcfg.BrokerURL
	}
	if brokerURL != "" {
		probeBroker(ctx, brokerURL, opts.Log)
	}
	return enabled, nil
}

// probeBroker checks the broker's readiness endpoint once at startup, purely
// advisorily: it surfaces a missing model credential or an unreachable
// broker in the controller's log without making deployment order fatal.
func probeBroker(ctx context.Context, brokerURL string, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	url := strings.TrimRight(brokerURL, "/") + "/readyz"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		log.Warn("egress broker probe failed", "url", url, "error", err)
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Warn("egress broker not reachable yet; brokered (claude/codex) jobs will fail until it is",
			"url", url, "error", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		log.Warn("egress broker is not ready; check its /readyz and credential sources",
			"url", url, "status", resp.StatusCode)
	}
}

// ResolveHarness resolves a model id (model.Lookup: exact, alias or pattern)
// to its harness and CLI model-id given the enabled set, erroring when the
// model is unknown or unrunnable.
func ResolveHarness(canonical string, enabled []string) (harnessID, cliModelID string, err error) {
	m, ok := model.Lookup(canonical)
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
