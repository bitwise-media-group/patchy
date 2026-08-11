// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package scan

import (
	"context"
	"encoding/json"
	"fmt"
)

// Grype shells out to grype, for stores that want its vulnerability
// database alongside (or instead of) the built-in scanner. The binary
// must be on PATH when the scanner is enabled.
type Grype struct {
	// Runner executes the binary (nil: the real PATH).
	Runner ToolRunner
}

// Name implements ImageScanner.
func (*Grype) Name() string { return "grype" }

// grypeOutput is the subset of grype's JSON report the mirror reads.
type grypeOutput struct {
	Matches []struct {
		Vulnerability struct {
			ID       string `json:"id"`
			Severity string `json:"severity"`
			Fix      struct {
				Versions []string `json:"versions"`
			} `json:"fix"`
		} `json:"vulnerability"`
		RelatedVulnerabilities []struct {
			ID string `json:"id"`
		} `json:"relatedVulnerabilities"`
		Artifact struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"artifact"`
	} `json:"matches"`
}

// ScanImage implements ImageScanner: grype -o json over the registry
// reference. The severity gate is the engine's, so grype never gates here
// — its JSON is the product.
func (g *Grype) ScanImage(ctx context.Context, ref string) ([]Finding, error) {
	runner := g.Runner
	if runner == nil {
		runner = ExecRunner{}
	}
	if !runner.Look("grype") {
		return nil, fmt.Errorf("grype is enabled but not on PATH")
	}
	stdout, err := runner.Run(ctx, "grype", []string{"--quiet", "-o", "json", "registry:" + ref}, nil)
	if err != nil && len(stdout) == 0 {
		return nil, fmt.Errorf("grype scan of %s: %w", ref, err)
	}
	var out grypeOutput
	if err := json.Unmarshal(stdout, &out); err != nil {
		return nil, fmt.Errorf("parse grype output for %s: %w", ref, err)
	}
	var findings []Finding
	for _, m := range out.Matches {
		f := Finding{
			ID:        m.Vulnerability.ID,
			Severity:  NormalizeSeverity(m.Vulnerability.Severity),
			Package:   m.Artifact.Name,
			Installed: m.Artifact.Version,
			FixedIn:   append([]string(nil), m.Vulnerability.Fix.Versions...),
			Scanner:   "grype",
		}
		for _, rel := range m.RelatedVulnerabilities {
			if rel.ID != "" && rel.ID != f.ID {
				f.Aliases = append(f.Aliases, rel.ID)
			}
		}
		findings = append(findings, f)
	}
	return findings, nil
}
