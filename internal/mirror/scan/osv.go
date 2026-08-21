// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package scan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"

	"github.com/bitwise-media-group/patchy/internal/mirror/ocireg"
)

// OSV shells out to osv-scanner: the image is exported to a local archive
// with the same registry client everything else uses, then scanned by the
// binary (which must be on PATH when the scanner is enabled) against the
// OSV.dev API.
type OSV struct {
	// Registry pulls the image (nil: ambient keychain).
	Registry *ocireg.Client
	// Runner executes the binary (nil: the real PATH).
	Runner ToolRunner
}

// Name implements ImageScanner.
func (*OSV) Name() string { return "osv" }

// osv-scanner's meaningful exit codes: 1 is the vulnerabilities-found
// sentinel (a successful scan; it only drives the upstream CLI's exit),
// 128 means nothing extractable (scratch/distroless — clean, not a
// failure).
const (
	osvExitVulnsFound = 1
	osvExitNoPackages = 128
)

// ScanImage implements ImageScanner.
func (o *OSV) ScanImage(ctx context.Context, ref string) ([]Finding, error) {
	runner := o.Runner
	if runner == nil {
		runner = ExecRunner{}
	}
	if !runner.Look("osv-scanner") {
		return nil, errors.New("osv-scanner is enabled but not on PATH; install it " +
			"(mise install / brew install osv-scanner) or set scan.scanners.osv.enabled: false in mirror.yaml")
	}
	reg := o.Registry
	if reg == nil {
		reg = ocireg.New(nil)
	}
	dir, err := os.MkdirTemp("", "patchy-mirror-osv-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	archive := filepath.Join(dir, "image.tar")
	if err := reg.SaveTarball(ctx, ref, archive); err != nil {
		return nil, err
	}
	// --format json routes logs to stderr and the report to stdout.
	stdout, err := runner.Run(ctx, "osv-scanner",
		[]string{"scan", "image", "--archive", "--format", "json", archive}, nil)
	switch code := exitStatus(err); {
	case err == nil, code == osvExitVulnsFound:
	case code == osvExitNoPackages:
		return nil, nil
	default:
		// Any other error means the scan is not authoritative — even
		// with partial results, reporting them as complete would turn a
		// broken scan into false assurance.
		return nil, fmt.Errorf("osv scan of %s: %w", ref, err)
	}
	var out osvOutput
	if err := json.Unmarshal(stdout, &out); err != nil {
		return nil, fmt.Errorf("parse osv-scanner output for %s: %w", ref, err)
	}
	return osvFindings(out), nil
}

// exitStatus extracts a subprocess exit code from a (wrapped) run error;
// -1 when err carries none.
func exitStatus(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// osvOutput is the subset of osv-scanner's JSON report the mirror reads
// (the serialized form of its VulnerabilityResults model).
type osvOutput struct {
	Results []struct {
		Packages []osvPackage `json:"packages"`
	} `json:"results"`
}

// osvPackage is one scanned package with its grouped vulnerabilities.
type osvPackage struct {
	Package struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"package"`
	Groups          []osvGroup         `json:"groups"`
	Vulnerabilities []osvVulnerability `json:"vulnerabilities"`
}

// osvGroup carries a related-advisory set's alias list and max severity.
type osvGroup struct {
	IDs         []string `json:"ids"`
	Aliases     []string `json:"aliases"`
	MaxSeverity string   `json:"max_severity"`
}

// osvVulnerability is one advisory with its fixed-version events.
type osvVulnerability struct {
	ID       string   `json:"id"`
	Aliases  []string `json:"aliases"`
	Affected []struct {
		Package struct {
			Name string `json:"name"`
		} `json:"package"`
		Ranges []struct {
			Events []struct {
				Fixed string `json:"fixed"`
			} `json:"events"`
		} `json:"ranges"`
	} `json:"affected"`
}

// osvFindings flattens osv results to the neutral shape.
func osvFindings(out osvOutput) []Finding {
	var findings []Finding
	for _, source := range out.Results {
		for _, pkg := range source.Packages {
			// Group metadata carries the alias sets and max severity;
			// index them by member ID.
			groupOf := map[string]osvGroup{}
			for _, g := range pkg.Groups {
				for _, id := range g.IDs {
					groupOf[id] = g
				}
			}
			for _, vuln := range pkg.Vulnerabilities {
				f := Finding{
					ID:        vuln.ID,
					Aliases:   append([]string(nil), vuln.Aliases...),
					Package:   pkg.Package.Name,
					Installed: pkg.Package.Version,
					FixedIn:   osvFixedVersions(vuln, pkg.Package.Name),
					Scanner:   "osv",
					Severity:  "UNKNOWN",
				}
				if g, ok := groupOf[vuln.ID]; ok {
					f.Severity = SeverityFromScore(g.MaxSeverity)
					// The group's aliases cover every related ID family.
					f.Aliases = append(f.Aliases, g.Aliases...)
				}
				f.Aliases = dedupeStrings(f.Aliases, f.ID)
				findings = append(findings, f)
			}
		}
	}
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].ID != findings[j].ID {
			return findings[i].ID < findings[j].ID
		}
		return findings[i].Package < findings[j].Package
	})
	return findings
}

// osvFixedVersions collects the advisory's fixed events for the package.
func osvFixedVersions(vuln osvVulnerability, pkgName string) []string {
	var fixed []string
	for _, affected := range vuln.Affected {
		if affected.Package.Name != "" && affected.Package.Name != pkgName {
			continue
		}
		for _, r := range affected.Ranges {
			for _, ev := range r.Events {
				if ev.Fixed != "" {
					fixed = append(fixed, ev.Fixed)
				}
			}
		}
	}
	return dedupeStrings(fixed, "")
}

// dedupeStrings removes duplicates and the excluded value, preserving
// order.
func dedupeStrings(in []string, exclude string) []string {
	seen := map[string]bool{exclude: true}
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
