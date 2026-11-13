// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package scan

import (
	"context"
	"fmt"
	"os/exec"
	"slices"
	"strconv"
	"strings"

	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
)

// Finding is one vulnerability in one image, scanner-neutral.
type Finding struct {
	// ID is the scanner's primary identifier (CVE, GHSA, GO-...).
	ID string `json:"id"`
	// Aliases are equivalent identifiers from other ID families.
	Aliases []string `json:"aliases,omitempty"`
	// Severity is normalized: CRITICAL, HIGH, MEDIUM, LOW, NEGLIGIBLE or
	// UNKNOWN.
	Severity string `json:"severity"`
	// Package is the affected artifact.
	Package string `json:"package"`
	// Installed is the version the image ships.
	Installed string `json:"installed"`
	// FixedIn lists released versions carrying the fix.
	FixedIn []string `json:"fixedIn,omitempty"`
	// Scanner names the reporter (osv, grype).
	Scanner string `json:"scanner"`
	// Locations are the in-image paths the package was found in — the
	// binary a Go module is compiled into, the lockfile or package
	// database that declares it — sorted and deduplicated.
	Locations []string `json:"locations,omitempty"`
	// Image is the reference the finding was reported against, stamped by
	// the caller when one scan covers several images (a mirror entry);
	// empty when the scanned image is implied (a build entry).
	Image string `json:"image,omitempty"`
}

// Fixed reports whether any fixed version is known.
func (f Finding) Fixed() bool { return len(f.FixedIn) > 0 }

// MergeLocations unions two location lists, sorted and deduplicated — for
// callers collapsing repeat reports of one finding.
func MergeLocations(a, b []string) []string {
	return sortedUnique(append(slices.Clone(a), b...))
}

// sortedUnique returns in sorted with duplicates and empties removed; nil
// when nothing remains, so an absent location stays omitted in JSON.
func sortedUnique(in []string) []string {
	out := slices.DeleteFunc(slices.Clone(in), func(s string) bool { return s == "" })
	slices.Sort(out)
	out = slices.Compact(out)
	if len(out) == 0 {
		return nil
	}
	return out
}

// ImageScanner scans one image reference for vulnerabilities.
type ImageScanner interface {
	// Name identifies the scanner in findings and narration.
	Name() string
	// ScanImage scans ref (registry reference, usually digest-pinned).
	ScanImage(ctx context.Context, ref string) ([]Finding, error)
}

// ArchiveScanner scans a local docker-archive tarball — an image that
// exists only on disk, like a fresh build no registry has seen yet.
type ArchiveScanner interface {
	// Name identifies the scanner in findings and narration.
	Name() string
	// ScanArchive scans the docker archive at path.
	ScanArchive(ctx context.Context, path string) ([]Finding, error)
}

// Blocking applies a resolved policy's blocking rule: severity in failOn,
// and — under ignoreUnfixed — only findings a released fix exists for.
// Allowlist suppression (Suppress) is a separate, later step.
func Blocking(findings []Finding, policy spec.ScanPolicy) []Finding {
	var out []Finding
	for _, f := range findings {
		if !SeverityIn(f.Severity, policy.FailOn) {
			continue
		}
		if policy.IgnoreUnfixed && !f.Fixed() {
			continue
		}
		out = append(out, f)
	}
	return out
}

// ToolRunner executes external scanner binaries; a seam so tests can can
// their output.
type ToolRunner interface {
	// Run executes name with args and extra environment, returning
	// stdout. A non-zero exit with useful stdout (grype gating) is NOT
	// an error here; runners return stdout and the exit error together.
	Run(ctx context.Context, name string, args, env []string) (stdout []byte, err error)
	// Look reports whether the binary is available.
	Look(name string) bool
}

// ExecRunner runs tools on the real PATH.
type ExecRunner struct{}

// Run implements ToolRunner.
func (ExecRunner) Run(ctx context.Context, name string, args, env []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(cmd.Environ(), env...)
	var stdout strings.Builder
	cmd.Stdout = &stdout
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil && stdout.Len() == 0 {
		return nil, fmt.Errorf("%s: %w (%s)", name, err, strings.TrimSpace(stderr.String()))
	}
	return []byte(stdout.String()), err
}

// Look implements ToolRunner.
func (ExecRunner) Look(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// The normalized severity ladder, most severe first.
var severityOrder = []string{"CRITICAL", "HIGH", "MEDIUM", "LOW", "NEGLIGIBLE", "UNKNOWN"}

// NormalizeSeverity maps scanner severity spellings ("Critical", "high")
// onto the normalized ladder.
func NormalizeSeverity(s string) string {
	up := strings.ToUpper(strings.TrimSpace(s))
	for _, level := range severityOrder {
		if up == level {
			return level
		}
	}
	return "UNKNOWN"
}

// SeverityFromScore maps a CVSS score onto the normalized ladder, using
// the standard v3 bands.
func SeverityFromScore(score string) string {
	v, err := strconv.ParseFloat(strings.TrimSpace(score), 64)
	if err != nil {
		return "UNKNOWN"
	}
	switch {
	case v >= 9.0:
		return "CRITICAL"
	case v >= 7.0:
		return "HIGH"
	case v >= 4.0:
		return "MEDIUM"
	case v > 0:
		return "LOW"
	default:
		return "NEGLIGIBLE"
	}
}

// SeverityIn reports whether severity is in the (normalized) list.
func SeverityIn(severity string, list []string) bool {
	for _, s := range list {
		if NormalizeSeverity(s) == severity {
			return true
		}
	}
	return false
}

// Suppress splits findings into kept and allowlisted, matching an entry's
// ID against each finding's ID and aliases — so an allowlist written
// against one scanner's ID family keeps working under another.
func Suppress(findings []Finding, allow *spec.Allowlist) (kept, suppressed []Finding) {
	if allow == nil || len(allow.Vulnerabilities) == 0 {
		return findings, nil
	}
	allowed := map[string]bool{}
	for _, e := range allow.Vulnerabilities {
		if e.ID != "" {
			allowed[strings.ToUpper(e.ID)] = true
		}
	}
	for _, f := range findings {
		if matchesAllowed(f, allowed) {
			suppressed = append(suppressed, f)
		} else {
			kept = append(kept, f)
		}
	}
	return kept, suppressed
}

// matchesAllowed checks a finding's ID and aliases against the allow set.
func matchesAllowed(f Finding, allowed map[string]bool) bool {
	if allowed[strings.ToUpper(f.ID)] {
		return true
	}
	for _, a := range f.Aliases {
		if allowed[strings.ToUpper(a)] {
			return true
		}
	}
	return false
}
