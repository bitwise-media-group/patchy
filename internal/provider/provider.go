// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package provider

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/bitwise-media-group/patchy/internal/harness"
	"github.com/bitwise-media-group/patchy/internal/model"
)

// BrokerTokenHeader carries the agent pod's projected ServiceAccount token
// to the egress broker; the broker validates it via TokenReview and strips
// it before any byte is forwarded upstream. It is defined here, the one
// package both the in-pod runtime and the broker import, so the two sides
// can never drift.
const BrokerTokenHeader = "X-Patchy-Broker-Token"

// The claude CLI's first-party route refuses to send any request ("Not logged
// in · Please run /login") unless one of its credential channels is set.
// Unlike bedrock/vertex/foundry, it has no CLAUDE_CODE_SKIP_*_AUTH switch. A
// brokered anthropic run therefore presents this fixed, non-secret stand-in on
// the child process's env (never the pod spec, which stays credential-free).
// The broker strips Authorization before injecting the real credential, so
// the value never leaves the cluster and grants nothing.
const (
	BrokerAuthPlaceholderEnv = "ANTHROPIC_AUTH_TOKEN"
	BrokerAuthPlaceholder    = "patchy-egress-broker"
)

// Provider names: the model APIs a brokered runner can front. The first four
// serve the claude harness, the last two the codex harness.
const (
	Anthropic = "anthropic"
	Bedrock   = "bedrock"
	Vertex    = "vertex"
	Foundry   = "foundry"
	OpenAI    = "openai"
	Azure     = "azure"
)

// Names lists the valid claude provider names, in display order.
var Names = []string{Anthropic, Bedrock, Vertex, Foundry}

// CodexNames lists the valid codex provider names, in display order.
var CodexNames = []string{OpenAI, Azure}

// NamesFor lists the valid provider names for a harness.
func NamesFor(harnessID string) []string {
	if harnessID == model.HarnessCodex {
		return CodexNames
	}
	return Names
}

// Config describes one brokered runner's model provider.
type Config struct {
	// Harness is the harness the runner drives: claude (the default when
	// empty) or codex. It selects the valid provider names and how the
	// gateway env is rendered.
	Harness string
	// Name is the provider: anthropic, bedrock, vertex, or foundry for
	// claude; openai or azure for codex.
	Name string
	// BrokerURL is the egress broker's base URL; the per-provider route
	// (/anthropic, /bedrock, /openai, ...) is appended. Always required,
	// because brokered is the only mode a claude or codex runner has.
	BrokerURL string
	// Region is the provider region: required for bedrock (SigV4 scope and
	// the AWS_REGION the CLI sends) and vertex (CLOUD_ML_REGION).
	Region string
	// RegionPrefix overrides the Bedrock inference-profile prefix derived
	// from Region (us/eu/apac) for regions the derivation cannot cover.
	RegionPrefix string
	// ProjectID is the GCP project Vertex requests bill to; vertex only.
	ProjectID string
	// ModelMap maps canonical model ids to provider-specific CLI ids,
	// overriding the derived defaults. Foundry and Azure have no derivable
	// defaults (deployment names are operator-chosen), so there it is the
	// whole map.
	ModelMap map[string]string
	// ExtraEnv is operator passthrough into the agent pod's gateway env.
	// Credential-named and patchy-owned keys are rejected.
	ExtraEnv map[string]string
}

// GatewayEnvNames are the agent-pod env names this package owns. internal/jobs
// reserves all of them so controller-global env can never shadow a gateway
// value, and ExtraEnv may not name them either.
var GatewayEnvNames = []string{
	"ANTHROPIC_BASE_URL",
	"ANTHROPIC_BEDROCK_BASE_URL",
	"ANTHROPIC_VERTEX_BASE_URL",
	"ANTHROPIC_FOUNDRY_BASE_URL",
	"CLAUDE_CODE_USE_BEDROCK",
	"CLAUDE_CODE_USE_VERTEX",
	"CLAUDE_CODE_USE_FOUNDRY",
	"CLAUDE_CODE_SKIP_BEDROCK_AUTH",
	"CLAUDE_CODE_SKIP_VERTEX_AUTH",
	"CLAUDE_CODE_SKIP_FOUNDRY_AUTH",
	"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC",
	"ANTHROPIC_CUSTOM_HEADERS",
	"ANTHROPIC_VERTEX_PROJECT_ID",
	"CLOUD_ML_REGION",
	"AWS_REGION",
	"PATCHY_BROKER_TOKEN_FILE",
	"PATCHY_MODEL_MAP",
	CodexConfigEnv,
	BrokerTokenEnv,
	CodexPlaceholderKeyEnv,
}

func (c Config) harnessID() string {
	if c.Harness == "" {
		return model.HarnessClaude
	}
	return c.Harness
}

// Validate rejects a config the gateway env cannot be built from.
func (c Config) Validate() error {
	h := c.harnessID()
	if h != model.HarnessClaude && h != model.HarnessCodex {
		return fmt.Errorf("harness %q cannot be brokered", h)
	}
	if names := NamesFor(h); !slices.Contains(names, c.Name) {
		return fmt.Errorf("%s provider %q is not one of %s", h, c.Name, strings.Join(names, ", "))
	}
	if c.BrokerURL == "" {
		return fmt.Errorf("provider %s: broker URL is required (%s runs proxy-only)", c.Name, h)
	}
	if err := checkTOMLSafe(c.BrokerURL); err != nil {
		return fmt.Errorf("provider %s: broker URL: %w", c.Name, err)
	}
	if c.Name == Bedrock && c.Region == "" {
		return fmt.Errorf("provider %s: region is required", c.Name)
	}
	if c.Name == Vertex && (c.Region == "" || c.ProjectID == "") {
		return fmt.Errorf("provider %s: region and project id are required", c.Name)
	}
	forbidden := forbiddenEnvNames()
	for _, k := range slices.Sorted(maps.Keys(c.ExtraEnv)) {
		if forbidden[k] {
			return fmt.Errorf("provider env %s is patchy-owned or a credential channel; it cannot be set here", k)
		}
	}
	return nil
}

// forbiddenEnvNames is what ExtraEnv may not name: every gateway name this
// package emits plus every brokerable harness's own credential channels. A
// credential belongs in the broker, never in the pod.
func forbiddenEnvNames() map[string]bool {
	out := map[string]bool{}
	for _, k := range GatewayEnvNames {
		out[k] = true
	}
	for _, k := range harness.NewClaude().EnvKeys() {
		out[k] = true
	}
	for _, k := range harness.NewCodex().EnvKeys() {
		out[k] = true
	}
	return out
}

// Env renders the agent-pod gateway environment for the provider: for
// claude, the base-URL override pointing the CLI at the broker's provider
// route, the skip-auth switches (the broker owns the credential) and the
// provider's own context vars; for codex, the rendered config.toml
// (CodexConfig). Either way it adds the rendered model map and the ExtraEnv
// passthrough. modelMap is the effective canonical→CLI-id map
// (EffectiveModelMap).
func Env(c Config, modelMap map[string]string) map[string]string {
	base := strings.TrimRight(c.BrokerURL, "/")
	if c.harnessID() == model.HarnessCodex {
		env := map[string]string{CodexConfigEnv: CodexConfig(c)}
		if len(modelMap) > 0 {
			env["PATCHY_MODEL_MAP"] = FormatModelMap(modelMap)
		}
		maps.Copy(env, c.ExtraEnv)
		return env
	}
	env := map[string]string{
		// The pod never talks to a version-check or telemetry endpoint; the
		// broker is its only egress and only proxies model traffic.
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
	}
	switch c.Name {
	case Anthropic:
		env["ANTHROPIC_BASE_URL"] = base + "/anthropic"
	case Bedrock:
		env["CLAUDE_CODE_USE_BEDROCK"] = "1"
		env["CLAUDE_CODE_SKIP_BEDROCK_AUTH"] = "1"
		env["ANTHROPIC_BEDROCK_BASE_URL"] = base + "/bedrock"
		env["AWS_REGION"] = c.Region
	case Vertex:
		env["CLAUDE_CODE_USE_VERTEX"] = "1"
		env["CLAUDE_CODE_SKIP_VERTEX_AUTH"] = "1"
		env["ANTHROPIC_VERTEX_BASE_URL"] = base + "/vertex"
		env["ANTHROPIC_VERTEX_PROJECT_ID"] = c.ProjectID
		env["CLOUD_ML_REGION"] = c.Region
	case Foundry:
		env["CLAUDE_CODE_USE_FOUNDRY"] = "1"
		env["CLAUDE_CODE_SKIP_FOUNDRY_AUTH"] = "1"
		env["ANTHROPIC_FOUNDRY_BASE_URL"] = base + "/foundry"
	}
	if len(modelMap) > 0 {
		env["PATCHY_MODEL_MAP"] = FormatModelMap(modelMap)
	}
	maps.Copy(env, c.ExtraEnv)
	return env
}

// EffectiveModelMap resolves the canonical→CLI-id map the pod translates
// with: a derived default per registry model the claude harness can run,
// overridden by the operator's entries. The first-party providers (anthropic,
// openai) need no translation because the registry already carries their
// CLIs' ids, and foundry and azure have no derivable defaults, so all four
// return a copy of cfg.ModelMap.
func EffectiveModelMap(c Config, models []model.Model) (map[string]string, error) {
	switch c.Name {
	case Anthropic, Foundry, OpenAI, Azure:
		return maps.Clone(c.ModelMap), nil
	}
	out := map[string]string{}
	for _, m := range models {
		if !m.Supports(model.HarnessClaude) {
			continue
		}
		switch c.Name {
		case Bedrock:
			prefix, err := bedrockPrefix(c)
			if err != nil {
				return nil, err
			}
			// Bedrock cross-region inference-profile ids:
			// <geo prefix>.anthropic.<vendor model id>.
			out[m.ID] = prefix + ".anthropic." + m.BareID()
		case Vertex:
			// Vertex publisher-model ids are the vendor's own bare ids.
			out[m.ID] = m.BareID()
		}
	}
	maps.Copy(out, c.ModelMap)
	return out, nil
}

// bedrockPrefix resolves the inference-profile geo prefix: the operator's
// override, else derived from the region's geography.
func bedrockPrefix(c Config) (string, error) {
	if c.RegionPrefix != "" {
		return c.RegionPrefix, nil
	}
	switch {
	case strings.HasPrefix(c.Region, "us-"):
		return "us", nil
	case strings.HasPrefix(c.Region, "eu-"):
		return "eu", nil
	case strings.HasPrefix(c.Region, "ap-"):
		return "apac", nil
	}
	return "", fmt.Errorf("provider bedrock: cannot derive an inference-profile prefix from region %q; "+
		"set an explicit region prefix", c.Region)
}

// ValidateCoverage fails when a required canonical model (allowlist entries,
// stage defaults) that the config's harness would run has no entry in the
// effective map. Only foundry and azure can under-cover, since their maps are
// entirely operator-written, but the check is provider-agnostic so a future
// gap surfaces at startup rather than mid-run.
func ValidateCoverage(c Config, models []model.Model, modelMap map[string]string, required []string) error {
	if c.Name == Anthropic || c.Name == OpenAI {
		return nil // canonical ids are already the first-party CLI's ids
	}
	h := c.harnessID()
	var missing []string
	for _, id := range slices.Sorted(slices.Values(required)) {
		if id == "" {
			continue
		}
		m, ok := model.Resolve(models, id)
		if !ok || !m.Supports(h) {
			continue // another harness's model; not this runner's problem
		}
		if _, ok := modelMap[m.ID]; !ok {
			missing = append(missing, m.ID)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("provider %s: no model-map entry for %s", c.Name, strings.Join(slices.Compact(missing), ", "))
	}
	return nil
}

// FormatModelMap renders a model map as the PATCHY_MODEL_MAP wire form:
// comma-joined canonical=cliID pairs, sorted for determinism.
func FormatModelMap(m map[string]string) string {
	pairs := make([]string, 0, len(m))
	for _, k := range slices.Sorted(maps.Keys(m)) {
		pairs = append(pairs, k+"="+m[k])
	}
	return strings.Join(pairs, ",")
}

// ParseModelMap parses the PATCHY_MODEL_MAP wire form back into a map. It is
// the single codec both sides share; a malformed entry is an error, not a
// silently dropped translation.
func ParseModelMap(s string) (map[string]string, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	out := map[string]string{}
	for pair := range strings.SplitSeq(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		canonical, cliID, ok := strings.Cut(pair, "=")
		if !ok || canonical == "" || cliID == "" {
			return nil, fmt.Errorf("model map entry %q is not canonical=cli-id", pair)
		}
		out[canonical] = cliID
	}
	return out, nil
}
