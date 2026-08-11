// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package spec

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Entry is one discovered mirror entry: a chart or an artifact. Exactly one
// of Chart and Artifact is set, matching Kind.
type Entry struct {
	// Name is the entry's directory name, its identity everywhere.
	Name string
	// Kind is KindChart or KindArtifact.
	Kind string
	// Dir is the absolute path of the entry directory.
	Dir      string
	Chart    *ChartManifest
	Artifact *ArtifactManifest
}

// Lockstep returns the entry's update group, empty when it rides alone.
func (e Entry) Lockstep() string {
	if e.Chart != nil {
		return e.Chart.Chart.Lockstep
	}
	return e.Artifact.Artifact.Lockstep
}

// Version returns the entry's pinned upstream version.
func (e Entry) Version() string {
	if e.Chart != nil {
		return e.Chart.Chart.Version
	}
	return e.Artifact.Artifact.Version
}

// VersionConstraint returns the entry's update constraint.
func (e Entry) VersionConstraint() string {
	if e.Chart != nil {
		return e.Chart.Chart.VersionConstraint
	}
	return e.Artifact.Artifact.VersionConstraint
}

// Signing returns the entry's signing override, nil when it uses the global
// default.
func (e Entry) Signing() *Signing {
	if e.Chart != nil {
		return e.Chart.Signing
	}
	return e.Artifact.Signing
}

// ManifestPath is the entry's manifest.yaml path.
func (e Entry) ManifestPath() string { return filepath.Join(e.Dir, "manifest.yaml") }

// LockPath is the entry's lock file path: images.lock.yaml for charts,
// lock.yaml for artifacts.
func (e Entry) LockPath() string {
	if e.Kind == KindChart {
		return filepath.Join(e.Dir, "images.lock.yaml")
	}
	return filepath.Join(e.Dir, "lock.yaml")
}

// The entry-kind directories under the mirror root.
const (
	chartsDir    = "charts"
	artifactsDir = "artifacts"
)

// Discover globs charts/*/manifest.yaml and artifacts/*/manifest.yaml under
// root and loads every entry, sorted by name. The tree is the registry:
// nothing else lists entries.
func Discover(root string) ([]Entry, error) {
	var entries []Entry
	seen := map[string]string{}
	for _, d := range []struct{ dir, kind string }{
		{chartsDir, KindChart},
		{artifactsDir, KindArtifact},
	} {
		matches, err := filepath.Glob(filepath.Join(root, d.dir, "*", "manifest.yaml"))
		if err != nil {
			return nil, err
		}
		for _, m := range matches {
			e, err := loadEntry(m, d.kind)
			if err != nil {
				return nil, err
			}
			if other, dup := seen[e.Name]; dup {
				return nil, fmt.Errorf("entry name %q is claimed by both %s and %s", e.Name, other, e.Dir)
			}
			seen[e.Name] = e.Dir
			entries = append(entries, e)
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}

// LoadEntry loads one entry by name, wherever it lives.
func LoadEntry(root, name string) (Entry, error) {
	for _, d := range []struct{ dir, kind string }{
		{chartsDir, KindChart},
		{artifactsDir, KindArtifact},
	} {
		path := filepath.Join(root, d.dir, name, "manifest.yaml")
		if _, err := os.Stat(path); err == nil {
			return loadEntry(path, d.kind)
		}
	}
	return Entry{}, fmt.Errorf("no entry %q (expected %s or %s)",
		name,
		filepath.Join(root, chartsDir, name, "manifest.yaml"),
		filepath.Join(root, artifactsDir, name, "manifest.yaml"))
}

// loadEntry loads and cross-checks one manifest file.
func loadEntry(manifestPath, kind string) (Entry, error) {
	dir := filepath.Dir(manifestPath)
	e := Entry{Name: filepath.Base(dir), Kind: kind, Dir: dir}
	switch kind {
	case KindChart:
		m, err := LoadChartManifest(manifestPath)
		if err != nil {
			return Entry{}, err
		}
		if m.Name != e.Name {
			return Entry{}, fmt.Errorf("%s: manifest name %q must match its directory %q", manifestPath, m.Name, e.Name)
		}
		e.Chart = m
	case KindArtifact:
		m, err := LoadArtifactManifest(manifestPath)
		if err != nil {
			return Entry{}, err
		}
		if m.Name != e.Name {
			return Entry{}, fmt.Errorf("%s: manifest name %q must match its directory %q", manifestPath, m.Name, e.Name)
		}
		e.Artifact = m
	default:
		return Entry{}, fmt.Errorf("unknown entry kind %q", kind)
	}
	return e, nil
}
