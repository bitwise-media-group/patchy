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

// Phase selects which stages run.
type Phase string

// PhaseFull runs classification and (on a qualifying verdict) remediation;
// PhaseRemediate runs remediation only, from a controller-provided
// classification — the /approve re-run.
const (
	PhaseFull      Phase = "classify+remediate"
	PhaseRemediate Phase = "remediate"
)

// Config is the runner's configuration; in the pod every field arrives as
// PATCHY_* environment (see FromEnv).
type Config struct {
	Workspace string
	Repo      string
	Issue     int
	Phase     Phase

	ClassifyHarness  string
	RemediateHarness string
	ClassifyModel    string
	// RemediateModel is the model the remediation stage runs on when the
	// classification's suggestion is missing or not allowlisted.
	RemediateModel string
	ModelAllowlist []string

	// The classification stage's limits are absolute: it runs on exactly
	// these. The remediation stage's are ceilings — the classification
	// requests its own max_turns and token_budget, and Agent.clamp holds
	// those requests to these bounds.
	ClassifyMaxTurns     int
	ClassifyTokenBudget  int
	RemediateMaxTurns    int
	RemediateTokenBudget int
	ClassifyTimeout      time.Duration
	RemediateTimeout     time.Duration

	ConfidenceThreshold float64
	// DefaultBranch is the clone's base branch (bundle base).
	DefaultBranch  string
	BundleMaxBytes int

	// Out receives the envelope events (stdout in the pod).
	Out io.Writer
	Log *slog.Logger
}

// Workspace layout, derived from Config.Workspace.
func (c Config) repoDir() string   { return filepath.Join(c.Workspace, "repo") }
func (c Config) issuePath() string { return filepath.Join(c.Workspace, "input", "issue.md") }
func (c Config) inputClassification() string {
	return filepath.Join(c.Workspace, "input", "classification.md")
}
func (c Config) classificationPath() string {
	return filepath.Join(c.Workspace, "reports", "classification.md")
}
func (c Config) remediationPath() string {
	return filepath.Join(c.Workspace, "reports", "remediation.md")
}
func (c Config) commitScript() string { return filepath.Join(c.Workspace, "commit.sh") }
func (c Config) outDir() string       { return filepath.Join(c.Workspace, "out") }
func (c Config) bundlePath() string   { return filepath.Join(c.outDir(), "changeset.bundle") }
func (c Config) branch() string       { return fmt.Sprintf("patchy/issue-%d", c.Issue) }

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
		Workspace:        get("WORKSPACE", "/workspace"),
		Repo:             get("REPO", ""),
		Phase:            Phase(get("PHASE", string(PhaseFull))),
		ClassifyHarness:  get("CLASSIFY_HARNESS", "claude"),
		RemediateHarness: get("REMEDIATE_HARNESS", "claude"),
		ClassifyModel:    get("CLASSIFY_MODEL", "claude-sonnet-5"),
		RemediateModel:   get("REMEDIATE_MODEL", "claude-sonnet-5"),
		DefaultBranch:    get("DEFAULT_BRANCH", "main"),
	}
	if list := get("MODEL_ALLOWLIST", cfg.RemediateModel); list != "" {
		for _, m := range strings.Split(list, ",") {
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
	float := func(key, def string) float64 {
		v := get(key, def)
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			errs = append(errs, fmt.Sprintf("PATCHY_%s=%q is not a number", key, v))
		}
		return f
	}

	cfg.Issue = number("ISSUE", "0")
	cfg.ClassifyMaxTurns = number("CLASSIFY_MAX_TURNS", "25")
	cfg.ClassifyTokenBudget = number("CLASSIFY_TOKEN_BUDGET", "150000")
	cfg.RemediateMaxTurns = number("REMEDIATE_MAX_TURNS", "80")
	cfg.RemediateTokenBudget = number("REMEDIATE_TOKEN_BUDGET", "400000")
	cfg.BundleMaxBytes = number("BUNDLE_MAX_BYTES", strconv.Itoa(5<<20))
	cfg.ClassifyTimeout = duration("CLASSIFY_TIMEOUT", "15m")
	cfg.RemediateTimeout = duration("REMEDIATE_TIMEOUT", "45m")
	cfg.ConfidenceThreshold = float("CONFIDENCE_THRESHOLD", "0.75")

	if cfg.Repo == "" {
		errs = append(errs, "PATCHY_REPO is required")
	}
	if cfg.Issue <= 0 {
		errs = append(errs, "PATCHY_ISSUE is required")
	}
	if cfg.Phase != PhaseFull && cfg.Phase != PhaseRemediate {
		errs = append(errs, fmt.Sprintf("PATCHY_PHASE=%q is not %q or %q", cfg.Phase, PhaseFull, PhaseRemediate))
	}
	if len(errs) > 0 {
		return Config{}, fmt.Errorf("agentrun config: %s", strings.Join(errs, "; "))
	}

	cfg.Out = os.Stdout
	return cfg, nil
}
