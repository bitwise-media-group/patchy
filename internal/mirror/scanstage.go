// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package mirror

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/bitwise-media-group/patchy/internal/mirror/allowlist"
	"github.com/bitwise-media-group/patchy/internal/mirror/imageref"
	"github.com/bitwise-media-group/patchy/internal/mirror/scan"
	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
)

// ScanReport is one entry's vulnerability-scan outcome.
type ScanReport struct {
	// Blocking are findings at blocking severity that survived the
	// allowlist; any entry here fails the gate.
	Blocking []scan.Finding `json:"blocking,omitempty"`
	// Suppressed counts findings the allowlist accepted.
	Suppressed int `json:"suppressed"`
	// Scanned lists the references scanned.
	Scanned []string `json:"scanned,omitempty"`
	// ConfigScan reports the kubescape sweep: "clean", "findings",
	// "failed", or a skip reason.
	ConfigScan string `json:"configScan,omitempty"`
	// ConfigScanFailed blocks under kubescape fail mode.
	ConfigScanFailed bool `json:"configScanFailed,omitempty"`
}

// Failed reports whether the gate blocks.
func (r *ScanReport) Failed() bool { return len(r.Blocking) > 0 || r.ConfigScanFailed }

// Scan runs every enabled scanner over the entry's locked images (by
// digest), applies the blocking policy and the allowlist, and sweeps the
// rendered manifests for configuration findings.
func (e *Engine) Scan(ctx context.Context, entry spec.Entry) (*ScanReport, error) {
	report := &ScanReport{}
	if !e.global.Scan.EffectiveEnabled() {
		e.notef(entry.Name, "scan", "scanning disabled (scan.enabled: false)")
		return report, nil
	}
	refs, err := e.scanRefs(ctx, entry)
	if err != nil {
		return nil, err
	}
	report.Scanned = refs

	var policy spec.ScanPolicy
	if entry.Kind == spec.KindChart {
		policy = spec.EffectiveScanPolicy(e.global.Scan, entry.Chart.Scan)
	} else {
		policy = spec.EffectiveScanPolicy(e.global.Scan, nil)
	}
	allow, err := spec.LoadAllowlist(entry.Dir)
	if err != nil {
		return nil, err
	}

	findings, err := e.scanImages(ctx, entry, refs)
	if err != nil {
		return nil, err
	}
	blocking := filterBlocking(findings, policy)
	kept, suppressed := scan.Suppress(blocking, allow)
	report.Blocking = kept
	report.Suppressed = len(suppressed)

	if entry.Kind == spec.KindChart && e.global.Scan.Scanners.KubescapeEnabled() {
		mode := e.global.Scan.Scanners.KubescapeMode()
		e.notef(entry.Name, "scan", "scanning chart configuration (mode: %s)", mode)
		res, err := scan.Kubescape(ctx, e.tools, renderedPath(entry), mode)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", entry.Name, err)
		}
		switch {
		case res.Skipped != "":
			report.ConfigScan = "skipped: " + res.Skipped
			e.warnf(entry.Name, "scan", "configuration scan skipped: %s", res.Skipped)
		case res.Failed:
			report.ConfigScan = "failed"
			report.ConfigScanFailed = true
		default:
			report.ConfigScan = "clean"
		}
	}
	for _, f := range report.Blocking {
		e.warnf(entry.Name, "scan", "blocking: %s %s (%s %s, fixed in %v)",
			f.ID, f.Severity, f.Package, f.Installed, f.FixedIn)
	}
	return report, nil
}

// scanRefs lists the digest-pinned references an entry's scan covers.
func (e *Engine) scanRefs(ctx context.Context, entry spec.Entry) ([]string, error) {
	if entry.Kind == spec.KindChart {
		lock, err := spec.LoadImagesLock(entry.LockPath())
		if err != nil {
			return nil, fmt.Errorf("%s: no lock (run upgrade first): %w", entry.Name, err)
		}
		var refs []string
		for _, img := range lock.Images {
			src, err := imageref.Parse(img.Source)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", entry.Name, err)
			}
			refs = append(refs, e.rewrite(src.Repository)+"@"+img.Digest)
		}
		return refs, nil
	}

	lock, err := spec.LoadArtifactLock(entry.LockPath())
	if err != nil {
		return nil, fmt.Errorf("%s: no lock (run upgrade first): %w", entry.Name, err)
	}
	ref := e.rewrite(lock.Artifact.Ref) + "@" + lock.Artifact.Digest
	switch entry.Artifact.Scan.EffectiveEnabled() {
	case "false":
		return nil, nil
	case "true":
		return []string{ref}, nil
	default: // auto: scan iff the artifact is a runnable image
		runnable, err := e.runnableImage(ctx, ref)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", entry.Name, err)
		}
		if !runnable {
			e.notef(entry.Name, "scan", "not a runnable image; skipping the vulnerability scan")
			return nil, nil
		}
		return []string{ref}, nil
	}
}

// runnableImage reports whether ref is a container image (as opposed to a
// content artifact like a manifests bundle).
func (e *Engine) runnableImage(ctx context.Context, ref string) (bool, error) {
	mt, err := e.reg.ConfigMediaType(ctx, ref)
	if err != nil {
		return false, err
	}
	switch mt {
	case "application/vnd.oci.image.config.v1+json",
		"application/vnd.docker.container.image.v1+json":
		return true, nil
	}
	return false, nil
}

// scanImages fans every enabled scanner over every reference.
func (e *Engine) scanImages(ctx context.Context, entry spec.Entry, refs []string) ([]scan.Finding, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	scanners := e.scanners()
	if len(scanners) == 0 {
		return nil, fmt.Errorf("%s: no image scanner enabled: enable scan.scanners.osv or scan.scanners.grype "+
			"in mirror.yaml (or set scan.enabled: false)", entry.Name)
	}
	var all []scan.Finding
	for _, ref := range refs {
		for _, s := range scanners {
			e.notef(entry.Name, "scan", "scanning %s (%s)", ref, s.Name())
			findings, err := s.ScanImage(ctx, ref)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", entry.Name, err)
			}
			all = append(all, findings...)
		}
	}
	return all, nil
}

// scanners builds the enabled scanner roster.
func (e *Engine) scanners() []scan.ImageScanner {
	if e.imageScanners != nil {
		return e.imageScanners
	}
	var out []scan.ImageScanner
	if e.global.Scan.Scanners.OSVEnabled() {
		out = append(out, &scan.OSV{Registry: e.reg, Runner: e.tools})
	}
	if e.global.Scan.Scanners.GrypeEnabled() {
		out = append(out, &scan.Grype{Runner: e.tools})
	}
	return out
}

// filterBlocking applies the blocking policy: severity in failOn, and —
// under ignoreUnfixed — only findings a released fix exists for.
func filterBlocking(findings []scan.Finding, policy spec.ScanPolicy) []scan.Finding {
	var out []scan.Finding
	for _, f := range findings {
		if !scan.SeverityIn(f.Severity, policy.FailOn) {
			continue
		}
		if policy.IgnoreUnfixed && !f.Fixed() {
			continue
		}
		out = append(out, f)
	}
	return out
}

// AllowlistResult reports one derivation.
type AllowlistResult struct {
	// Generated is false when the entry does not opt in.
	Generated bool `json:"generated"`
	Kept      int  `json:"kept"`
	Added     int  `json:"added"`
	// Dropped lists IDs no longer reported.
	Dropped []string `json:"dropped,omitempty"`
}

// DeriveAllowlist regenerates the entry's security/allowlist.yaml from a
// fresh scan, for entries opting in with scan.allowlist.generate. Runs
// during upgrade only — the finding set moves with the vulnerability
// databases, so deriving inside validate would diff a tree nobody touched.
func (e *Engine) DeriveAllowlist(ctx context.Context, entry spec.Entry) (*AllowlistResult, error) {
	if entry.Kind != spec.KindChart || entry.Chart.Scan == nil || !entry.Chart.Scan.Allowlist.Generate {
		return &AllowlistResult{}, nil
	}
	if !e.global.Scan.EffectiveEnabled() {
		e.notef(entry.Name, "allowlist", "scanning disabled (scan.enabled: false); skipping derivation")
		return &AllowlistResult{}, nil
	}
	refs, err := e.scanRefs(ctx, entry)
	if err != nil {
		return nil, err
	}
	policy := spec.EffectiveScanPolicy(e.global.Scan, entry.Chart.Scan)
	findings, err := e.scanImages(ctx, entry, refs)
	if err != nil {
		return nil, err
	}
	blocking := filterBlocking(findings, policy)

	prev, err := spec.LoadAllowlist(entry.Dir)
	if err != nil {
		return nil, err
	}
	rows := make([]allowlist.Finding, 0, len(blocking))
	for _, f := range blocking {
		rows = append(rows, allowlist.Finding{
			ID: f.ID, Package: f.Package, Installed: f.Installed, FixedIn: f.FixedIn,
		})
	}
	entries, stats := allowlist.Derive(rows, prev, e.now(), e.global.Scan.EffectiveAllowlistNewDays())
	rendered := allowlist.Render(entries, entry.Chart.Scan.Allowlist.Preamble)
	path := filepath.Join(entry.Dir, spec.AllowlistFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if err := writeFile(path, rendered); err != nil {
		return nil, err
	}
	sort.Strings(stats.Dropped)
	for _, id := range stats.Dropped {
		e.notef(entry.Name, "allowlist", "dropping %s (no longer reported)", id)
	}
	e.notef(entry.Name, "allowlist", "wrote %s (%d kept, %d new, %d dropped)",
		spec.AllowlistFile, stats.Kept, stats.Added, len(stats.Dropped))
	return &AllowlistResult{Generated: true, Kept: stats.Kept, Added: stats.Added, Dropped: stats.Dropped}, nil
}
