// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package agentrun

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Phase selects which stage runs.
type Phase string

// PhaseInvestigate runs the analysis stage only; PhaseRemediate remediates
// from a controller-provided analysis file.
const (
	PhaseInvestigate Phase = "investigate"
	PhaseRemediate   Phase = "remediate"
)

// Config is the runner's configuration; in the pod every field arrives as
// PATCHY_* environment (see FromEnv).
type Config struct {
	Workspace string
	Repo      string
	// Finding names the owning Finding resource; events echo it so the
	// controller can key them.
	Finding string
	// BaseSHA is the remote commit the workspace tree corresponds to. The
	// local git base is a synthetic commit over the artifact tarball, so the
	// changeset's push base must come from here instead.
	BaseSHA string
	Phase   Phase

	InvestigateHarness string
	RemediateHarness   string
	InvestigateModel   string
	// RemediateModel is the model the remediation stage runs on when the
	// investigation's suggestion is missing or not allowlisted.
	RemediateModel string
	ModelAllowlist []string

	// The investigation stage's limits are absolute: it runs on exactly
	// these. The remediation stage's are ceilings — the investigation
	// requests its own max_turns and token_budget, and Agent.clamp holds
	// those requests to these bounds.
	InvestigateMaxTurns    int
	InvestigateTokenBudget int
	RemediateMaxTurns      int
	RemediateTokenBudget   int
	InvestigateTimeout     time.Duration
	RemediateTimeout       time.Duration

	// ChangesetMaxBytes caps the cumulative raw content of the changeset the
	// remediation stage may emit.
	ChangesetMaxBytes int

	// Out receives the envelope events (stdout in the pod).
	Out io.Writer
	Log *slog.Logger
}

// Workspace layout, derived from Config.Workspace.
func (c Config) repoDir() string   { return filepath.Join(c.Workspace, "repo") }
func (c Config) issuePath() string { return filepath.Join(c.Workspace, "input", "issue.md") }

func (c Config) inputInvestigation() string {
	return filepath.Join(c.Workspace, "input", "investigation.md")
}

func (c Config) investigationPath() string {
	return filepath.Join(c.Workspace, "reports", "investigation.md")
}

func (c Config) remediationPath() string {
	return filepath.Join(c.Workspace, "reports", "remediation.md")
}
func (c Config) commitScript() string { return filepath.Join(c.Workspace, "commit.sh") }

// branch is the remediation branch, keyed by finding name (pull-request
// webhooks resolve the Finding from the head ref).
func (c Config) branch() string { return "patchy/" + c.Finding }

// FromEnv builds the pod configuration from PATCHY_* environment variables,
// applying defaults.
func FromEnv(getenv func(string) string) (Config, error) {
	get := func(key, def string) string {
		if v := getenv("PATCHY_" + key); v != "" {
			return v
		}
		return def
	}

	cfg := Config{
		Workspace:          get("WORKSPACE", "/workspace"),
		Repo:               get("REPO", ""),
		Finding:            get("FINDING", ""),
		BaseSHA:            get("BASE_SHA", ""),
		Phase:              Phase(get("PHASE", string(PhaseInvestigate))),
		InvestigateHarness: get("INVESTIGATE_HARNESS", "claude"),
		RemediateHarness:   get("REMEDIATE_HARNESS", "claude"),
		InvestigateModel:   get("INVESTIGATE_MODEL", "claude-sonnet-5"),
		RemediateModel:     get("REMEDIATE_MODEL", "claude-sonnet-5"),
	}
	if list := get("MODEL_ALLOWLIST", cfg.RemediateModel); list != "" {
		for m := range strings.SplitSeq(list, ",") {
			if m = strings.TrimSpace(m); m != "" {
				cfg.ModelAllowlist = append(cfg.ModelAllowlist, m)
			}
		}
	}

	var errs []string
	number := func(key, def string) int {
		v := get(key, def)
		n, err := strconv.Atoi(v)
		if err != nil {
			errs = append(errs, fmt.Sprintf("PATCHY_%s=%q is not an integer", key, v))
		}
		return n
	}
	duration := func(key, def string) time.Duration {
		v := get(key, def)
		d, err := time.ParseDuration(v)
		if err != nil {
			errs = append(errs, fmt.Sprintf("PATCHY_%s=%q is not a duration", key, v))
		}
		return d
	}

	cfg.InvestigateMaxTurns = number("INVESTIGATE_MAX_TURNS", "25")
	cfg.InvestigateTokenBudget = number("INVESTIGATE_TOKEN_BUDGET", "150000")
	cfg.RemediateMaxTurns = number("REMEDIATE_MAX_TURNS", "80")
	cfg.RemediateTokenBudget = number("REMEDIATE_TOKEN_BUDGET", "400000")
	cfg.ChangesetMaxBytes = number("CHANGESET_MAX_BYTES", strconv.Itoa(5<<20))
	cfg.InvestigateTimeout = duration("INVESTIGATE_TIMEOUT", "15m")
	cfg.RemediateTimeout = duration("REMEDIATE_TIMEOUT", "45m")

	if cfg.Repo == "" {
		errs = append(errs, "PATCHY_REPO is required")
	}
	if cfg.Finding == "" {
		errs = append(errs, "PATCHY_FINDING is required")
	}
	switch cfg.Phase {
	case PhaseInvestigate, PhaseRemediate:
	default:
		errs = append(errs, fmt.Sprintf("PATCHY_PHASE=%q is not %q or %q",
			cfg.Phase, PhaseInvestigate, PhaseRemediate))
	}
	if len(errs) > 0 {
		return Config{}, fmt.Errorf("agentrun config: %s", strings.Join(errs, "; "))
	}

	cfg.Out = os.Stdout
	return cfg, nil
}
