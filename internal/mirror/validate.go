// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package mirror

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bitwise-media-group/patchy/internal/mirror/helmchart"
	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
)

// Validate stage names for --only filtering.
const (
	StageRegen  = "regen"
	StageVerify = "verify"
	StageScan   = "scan"
	StageLint   = "lint"
)

// ValidateStages is the full roster, in run order.
var ValidateStages = []string{StageRegen, StageVerify, StageScan, StageLint}

// ValidateResult is one entry's validation outcome.
type ValidateResult struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	// RegenDiffs lists derived files that no longer match a fresh
	// regeneration (the committed tree is stale — run upgrade).
	RegenDiffs []string `json:"regenDiffs,omitempty"`
	// Lint lists manifest/allowlist policy violations.
	Lint []string `json:"lint,omitempty"`
	// Verify is the upstream-provenance outcome.
	Verify *VerifyReport `json:"verify,omitempty"`
	// Scan is the vulnerability-scan outcome.
	Scan *ScanReport `json:"scan,omitempty"`
	// Err carries a stage failure (not a finding — the stage itself
	// broke or a verification failed).
	Err string `json:"error,omitempty"`
}

// Failed reports whether validation blocks.
func (r *ValidateResult) Failed() bool {
	return len(r.RegenDiffs) > 0 || len(r.Lint) > 0 || r.Err != "" || (r.Scan != nil && r.Scan.Failed())
}

// Validate proves one entry's committed state is current and clean without
// touching the tree: regenerate the derived state out-of-tree and
// byte-compare, verify upstream provenance, scan, and lint. only filters
// the stages (nil or empty runs all). Wall-clock steps (tracked picks,
// allowlist stamping) never run here — the byte-identity gate must stay
// deterministic.
func (e *Engine) Validate(ctx context.Context, entry spec.Entry, only []string) (*ValidateResult, error) {
	result := &ValidateResult{Name: entry.Name, Kind: entry.Kind}
	run := stageSet(only)

	if run[StageRegen] {
		diffs, err := e.validateRegen(ctx, entry)
		if err != nil {
			return nil, err
		}
		result.RegenDiffs = diffs
	}
	if run[StageVerify] {
		report, err := e.VerifyUpstream(ctx, entry)
		if err != nil {
			// A failed verification is the entry's failure, not the
			// engine's: record it so --all can keep going.
			result.Err = err.Error()
			return result, nil
		}
		result.Verify = report
	}
	if run[StageScan] {
		report, err := e.Scan(ctx, entry)
		if err != nil {
			return nil, err
		}
		result.Scan = report
	}
	if run[StageLint] {
		issues, err := e.Lint(entry)
		if err != nil {
			return nil, err
		}
		result.Lint = issues
	}
	return result, nil
}

// stageSet resolves the --only filter.
func stageSet(only []string) map[string]bool {
	set := map[string]bool{}
	if len(only) == 0 {
		for _, s := range ValidateStages {
			set[s] = true
		}
		return set
	}
	for _, s := range only {
		set[s] = true
	}
	return set
}

// validateRegen regenerates the derived state into scratch space and
// byte-compares it with the committed tree.
func (e *Engine) validateRegen(ctx context.Context, entry spec.Entry) ([]string, error) {
	scratch, err := os.MkdirTemp("", "patchy-mirror-validate-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(scratch) }()

	facts, err := e.Regenerate(ctx, entry, scratch, false)
	if err != nil {
		return nil, err
	}
	var diffs []string
	committedRendered, committedLock, err := ReadCommittedFacts(entry)
	if err != nil {
		return nil, err
	}
	if entry.Kind == spec.KindChart {
		treeDiffs, err := helmchart.TreeDiff(
			filepath.Join(scratch, "vendor", entry.Chart.Chart.Name),
			filepath.Join(vendorDir(entry), entry.Chart.Chart.Name))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", entry.Name, err)
		}
		for _, d := range treeDiffs {
			diffs = append(diffs, "vendor: "+d)
		}
		if !bytes.Equal(facts.Rendered, committedRendered) {
			diffs = append(diffs, "rendered/manifests.yaml differs from a fresh render")
		}
		if !bytes.Equal(facts.ImagesLock.Encode(), committedLock) {
			diffs = append(diffs, "images.lock.yaml differs from a fresh discovery")
		}
	} else {
		if !bytes.Equal(facts.ArtifactLock.Encode(), committedLock) {
			diffs = append(diffs, "lock.yaml differs from a fresh resolution")
		}
	}
	if len(diffs) > 0 {
		e.warnf(entry.Name, "validate", "derived state is stale (%d difference(s)); run upgrade", len(diffs))
	}
	return diffs, nil
}
