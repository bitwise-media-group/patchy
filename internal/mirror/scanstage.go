// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package mirror

import (
	"context"
	"fmt"
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
// digest), applies the entry's resolved blocking policy and the allowlist,
// and sweeps the rendered manifests for configuration findings.
func (e *Engine) Scan(ctx context.Context, entry spec.Entry) (*ScanReport, error) {
	report := &ScanReport{}
	policy := e.scanPolicy(entry)
	if !policy.Enabled {
		e.notef(entry.Name, "scan", "scanning disabled (%s)", disabledBy(policy))
		return report, nil
	}
	refs, err := e.scanRefs(ctx, entry, policy)
	if err != nil {
		return nil, err
	}
	report.Scanned = refs

	allow, err := spec.LoadAllowlist(entry.Dir)
	if err != nil {
		return nil, err
	}
	findings, err := e.scanImages(ctx, entry, refs)
	if err != nil {
		return nil, err
	}
	kept, suppressed := scan.Suppress(scan.Blocking(findings, policy), allow)
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

// scanOverride returns the entry's own scan block.
func scanOverride(entry spec.Entry) spec.ScanOverride {
	if entry.Kind == spec.KindChart {
		return entry.Chart.Scan
	}
	return entry.Artifact.Scan
}

// scanPolicy resolves the entry's scan block against the config's.
func (e *Engine) scanPolicy(entry spec.Entry) spec.ScanPolicy {
	return spec.EffectiveScanPolicy(e.global.Scan, scanOverride(entry))
}

// disabledBy names the switch that turned a resolved policy off.
func disabledBy(policy spec.ScanPolicy) string {
	if policy.Auto {
		return "scan.enabled: false in the patchy config"
	}
	return "the entry's scan.enabled: false"
}

// scanRefs lists the digest-pinned references an entry's scan covers: a
// chart's locked images, or the artifact itself — under enabled: auto only
// when it is a runnable image.
func (e *Engine) scanRefs(ctx context.Context, entry spec.Entry, policy spec.ScanPolicy) ([]string, error) {
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
	if !policy.Auto {
		return []string{ref}, nil
	}
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
			"in the patchy config (or set scan.enabled: false)", entry.Name)
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

// AllowlistResult reports one derivation.
type AllowlistResult struct {
	// Generated is false when the entry does not opt in.
	Generated bool `json:"generated"`
	Kept      int  `json:"kept"`
	// Renewed lists still-reported IDs whose lapsed expiry was restamped.
	Renewed []string `json:"renewed,omitempty"`
	Added   int      `json:"added"`
	// Dropped lists IDs no longer reported.
	Dropped []string `json:"dropped,omitempty"`
}

// DeriveAllowlist regenerates the entry's security/allowlist.yaml from a
// fresh scan, for charts and artifacts whose resolved scan.allowlist
// generate is true. Runs during upgrade only — the finding set moves with
// the vulnerability databases, so deriving inside validate would diff a
// tree nobody touched.
func (e *Engine) DeriveAllowlist(ctx context.Context, entry spec.Entry) (*AllowlistResult, error) {
	policy := e.scanPolicy(entry)
	if !policy.Allowlist.Generate {
		return &AllowlistResult{}, nil
	}
	if !policy.Enabled {
		e.notef(entry.Name, "allowlist", "scanning disabled (%s); skipping derivation", disabledBy(policy))
		return &AllowlistResult{}, nil
	}
	refs, err := e.scanRefs(ctx, entry, policy)
	if err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		e.notef(entry.Name, "allowlist", "nothing to scan; skipping derivation")
		return &AllowlistResult{}, nil
	}
	findings, err := e.scanImages(ctx, entry, refs)
	if err != nil {
		return nil, err
	}
	prev, err := spec.LoadAllowlist(entry.Dir)
	if err != nil {
		return nil, err
	}
	stats, err := allowlist.Regenerate(filepath.Join(entry.Dir, spec.AllowlistFile), allowlist.MirrorBanner,
		allowlist.Rows(scan.Blocking(findings, policy)), prev, e.now(),
		policy.Allowlist.NewDays, policy.Allowlist.Preamble)
	if err != nil {
		return nil, err
	}
	renewed := make([]string, 0, len(stats.Renewed))
	for _, r := range stats.Renewed {
		e.notef(entry.Name, "allowlist", "renewing %s (expired on %s)", r.ID, r.ExpiredAt)
		renewed = append(renewed, r.ID)
	}
	sort.Strings(stats.Dropped)
	for _, id := range stats.Dropped {
		e.notef(entry.Name, "allowlist", "dropping %s (no longer reported)", id)
	}
	e.notef(entry.Name, "allowlist", "wrote %s (%d kept, %d renewed, %d new, %d dropped)",
		spec.AllowlistFile, stats.Kept, len(stats.Renewed), stats.Added, len(stats.Dropped))
	return &AllowlistResult{
		Generated: true, Kept: stats.Kept, Renewed: renewed, Added: stats.Added, Dropped: stats.Dropped,
	}, nil
}
