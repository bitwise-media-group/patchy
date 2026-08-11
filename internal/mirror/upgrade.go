// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package mirror

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bitwise-media-group/patchy/internal/mirror/distro"
	"github.com/bitwise-media-group/patchy/internal/mirror/helmchart"
	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
	"github.com/bitwise-media-group/patchy/internal/mirror/yamledit"
)

// UpgradeResult reports one entry's convergence.
type UpgradeResult struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	// From and To are the pinned versions before and after (equal when
	// the pin did not move).
	From string `json:"from"`
	To   string `json:"to"`
	// Tracked reports the tracked-image picks applied.
	Tracked []TrackPlan `json:"tracked,omitempty"`
	// Changed lists entry-relative paths the upgrade rewrote. Empty
	// means the entry was already fully converged — a clean no-op.
	Changed []string `json:"changed,omitempty"`
	// Err carries a per-entry failure in --all summaries.
	Err string `json:"error,omitempty"`
}

// Upgrade converges one entry: splice the target pin (empty target keeps
// the current pin), re-pick tracked tags, then regenerate the derived
// state in place. Later pipeline stages (upstream verification, scanning,
// allowlist derivation) run on top of the regenerated tree.
func (e *Engine) Upgrade(ctx context.Context, entry spec.Entry, target string) (*UpgradeResult, error) {
	result := &UpgradeResult{Name: entry.Name, Kind: entry.Kind, From: entry.Version(), To: entry.Version()}
	before, err := snapshotTree(entry.Dir)
	if err != nil {
		return nil, err
	}

	if target != "" && target != entry.Version() {
		if err := e.SetVersion(entry, target); err != nil {
			return nil, err
		}
		result.To = target
		// Reload: the manifest on disk changed.
		entry, err = e.Entry(entry.Name)
		if err != nil {
			return nil, err
		}
	}

	if entry.Kind == spec.KindChart {
		tracked, err := e.ApplyTracks(ctx, entry)
		if err != nil {
			return nil, err
		}
		result.Tracked = tracked
	}

	facts, err := e.Regenerate(ctx, entry, entry.Dir, true)
	if err != nil {
		return nil, err
	}
	if err := WriteFacts(entry, facts); err != nil {
		return nil, err
	}

	// Allowlist derivation is the one scan the update path runs: the
	// regenerated file is what makes the validate gate green again, and
	// the PR review is where the risk is actually accepted.
	if _, err := e.DeriveAllowlist(ctx, entry); err != nil {
		return nil, err
	}

	after, err := snapshotTree(entry.Dir)
	if err != nil {
		return nil, err
	}
	result.Changed = diffSnapshots(before, after)
	if len(result.Changed) == 0 {
		e.notef(entry.Name, "upgrade", "already converged (no changes)")
	}
	return result, nil
}

// snapshotTree hashes every file under dir, keyed by relative slash path.
func snapshotTree(dir string) (map[string]string, error) {
	snap := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(raw)
		snap[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("snapshot %s: %w", dir, err)
	}
	return snap, nil
}

// diffSnapshots lists paths added, removed or rewritten between snapshots.
func diffSnapshots(before, after map[string]string) []string {
	var changed []string
	for p, h := range after {
		if before[p] != h {
			changed = append(changed, p)
		}
	}
	for p := range before {
		if _, ok := after[p]; !ok {
			changed = append(changed, p)
		}
	}
	sort.Strings(changed)
	return changed
}

// SetVersion splices a new pinned version into an entry's manifest.yaml,
// touching only the version token. The entry must be reloaded afterwards.
func (e *Engine) SetVersion(entry spec.Entry, version string) error {
	path := entry.ManifestPath()
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	yamlPath := ".chart.version"
	if entry.Kind == spec.KindArtifact {
		yamlPath = ".artifact.version"
	}
	edited, err := yamledit.Set(raw, yamlPath, entry.Version(), version)
	if err != nil {
		return fmt.Errorf("%s: splice version: %w", entry.Name, err)
	}
	if err := os.WriteFile(path, edited, 0o644); err != nil {
		return err
	}
	e.notef(entry.Name, "upgrade", "pinned %s -> %s", entry.Version(), version)
	return nil
}

// DeriveDistribution regenerates a chart entry's images.extra.yaml sidecar
// from its distribution manifests artifact, at the tag matching the
// vendored chart's appVersion. No-op (and nil result) for entries without
// a distributionManifests block. Returns the derived extras.
func (e *Engine) DeriveDistribution(ctx context.Context, entry spec.Entry) ([]spec.ExtraImage, error) {
	return e.deriveDistribution(ctx, entry, vendorDir(entry))
}

// deriveDistribution derives against a specific vendor tree (upgrade
// regenerates into scratch trees too).
func (e *Engine) deriveDistribution(ctx context.Context, entry spec.Entry, vendor string) ([]spec.ExtraImage, error) {
	if entry.Kind != spec.KindChart || entry.Chart.Images.DistributionManifests == nil {
		return nil, nil
	}
	dm := entry.Chart.Images.DistributionManifests
	appVersion, err := helmchart.AppVersion(vendor, entry.Chart.Chart.Name)
	if err != nil {
		return nil, fmt.Errorf("%s: no vendored appVersion to expand the artifact ref with: %w", entry.Name, err)
	}
	artifact := strings.ReplaceAll(dm.Artifact, "{appVersion}", appVersion)
	pullRef := e.rewrite(strings.TrimPrefix(artifact, "oci://"))

	e.notef(entry.Name, "discover", "pulling distribution manifests %s", artifact)
	var archive bytes.Buffer
	if err := e.reg.Export(ctx, pullRef, &archive); err != nil {
		return nil, fmt.Errorf("%s: %w", entry.Name, err)
	}
	extras, err := distro.Derive(&archive, dm.Components, dm.Registry, artifact)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", entry.Name, err)
	}
	sidecar := &spec.Sidecar{Extra: extras}
	if err := writeFile(filepath.Join(entry.Dir, spec.SidecarFile), sidecar.Encode()); err != nil {
		return nil, err
	}
	e.notef(entry.Name, "discover", "derived %d distribution image(s)", len(extras))
	return extras, nil
}
