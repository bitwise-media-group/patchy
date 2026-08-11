// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package scan

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/google/osv-scanner/v2/pkg/models"
	"github.com/google/osv-scanner/v2/pkg/osvscanner"
	osvschema "github.com/ossf/osv-schema/bindings/go/osvschema"

	"github.com/bitwise-media-group/patchy/internal/mirror/ocireg"
)

// OSV is the built-in library scanner: the image is exported to a local
// archive with the same registry client everything else uses, extracted by
// osv-scalibr, and matched against the OSV.dev API. No binary required.
type OSV struct {
	// Registry pulls the image (nil: ambient keychain).
	Registry *ocireg.Client
	// doScan is the scanner seam (nil: osvscanner.DoContainerScan).
	// DoContainerScan is the only entry point that reads the Image
	// action — DoScan silently ignores it and scans nothing.
	doScan func(osvscanner.ScannerActions) (models.VulnerabilityResults, error)
}

// Name implements ImageScanner.
func (*OSV) Name() string { return "osv" }

// ScanImage implements ImageScanner.
func (o *OSV) ScanImage(ctx context.Context, ref string) ([]Finding, error) {
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
	scanArchive := o.doScan
	if scanArchive == nil {
		scanArchive = osvscanner.DoContainerScan
	}
	results, err := scanArchive(osvscanner.ScannerActions{
		Image:          archive,
		IsImageArchive: true,
	})
	switch {
	case err == nil, errors.Is(err, osvscanner.ErrVulnerabilitiesFound):
		// Finding vulnerabilities is a successful scan; the sentinel
		// only drives the upstream CLI's exit code.
		return osvFindings(results), nil
	case errors.Is(err, osvscanner.ErrNoPackagesFound):
		// Nothing extractable (scratch/distroless): clean, not a failure.
		return nil, nil
	default:
		// Any other error means the scan is not authoritative — even
		// with partial results, reporting them as complete would turn a
		// broken scan into false assurance.
		return nil, fmt.Errorf("osv scan of %s: %w", ref, err)
	}
}

// osvFindings flattens osv results to the neutral shape.
func osvFindings(results models.VulnerabilityResults) []Finding {
	var findings []Finding
	for _, source := range results.Results {
		for _, pkg := range source.Packages {
			// Group metadata carries the alias sets and max severity;
			// index them by member ID.
			groupOf := map[string]models.GroupInfo{}
			for _, g := range pkg.Groups {
				for _, id := range g.IDs {
					groupOf[id] = g
				}
			}
			for _, vuln := range pkg.Vulnerabilities {
				f := Finding{
					ID:        vuln.GetId(),
					Aliases:   append([]string(nil), vuln.GetAliases()...),
					Package:   pkg.Package.Name,
					Installed: pkg.Package.Version,
					FixedIn:   osvFixedVersions(vuln, pkg.Package.Name),
					Scanner:   "osv",
					Severity:  "UNKNOWN",
				}
				if g, ok := groupOf[vuln.GetId()]; ok {
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
func osvFixedVersions(vuln *osvschema.Vulnerability, pkgName string) []string {
	var fixed []string
	for _, affected := range vuln.GetAffected() {
		if affected.GetPackage().GetName() != "" && affected.GetPackage().GetName() != pkgName {
			continue
		}
		for _, r := range affected.GetRanges() {
			for _, ev := range r.GetEvents() {
				if ev.GetFixed() != "" {
					fixed = append(fixed, ev.GetFixed())
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
