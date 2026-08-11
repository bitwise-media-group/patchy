// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package mirror

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"golang.org/x/sync/errgroup"

	"github.com/bitwise-media-group/patchy/internal/mirror/discover"
	"github.com/bitwise-media-group/patchy/internal/mirror/helmchart"
	"github.com/bitwise-media-group/patchy/internal/mirror/render"
	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
)

// Facts is the regenerated derived state of one entry, in memory. Callers
// choose where it lands: the entry directory (upgrade) or a scratch tree
// for byte-comparison (validate).
type Facts struct {
	// VendorTgz is the upstream chart archive (charts only).
	VendorTgz []byte
	// Rendered is the rendered manifest stream (charts only).
	Rendered []byte
	// ImagesLock is the chart lock (charts only).
	ImagesLock *spec.ImagesLock
	// ArtifactLock is the artifact lock (artifacts only).
	ArtifactLock *spec.ArtifactLock
	// Excluded lists image references dropped by exclude globs.
	Excluded []string
}

// Regenerate derives an entry's facts from its manifest: for charts, pull
// the pinned tgz, extract it into destDir/vendor, render with the
// discovery values, discover and digest-pin every referenced image; for
// artifacts, resolve the pinned version to a digest. destDir is where the
// vendor tree materializes (usually the entry dir; validate points it at
// scratch); the returned Facts carry everything else.
//
// deriveDistro re-derives the distribution-manifests sidecar between
// vendor and discovery (upgrade); when false the committed sidecar is
// used as-is (validate must not re-derive what the tree pins).
func (e *Engine) Regenerate(ctx context.Context, entry spec.Entry, destDir string, deriveDistro bool) (*Facts, error) {
	if entry.Kind == spec.KindArtifact {
		lock, err := e.regenerateArtifact(ctx, entry)
		if err != nil {
			return nil, err
		}
		return &Facts{ArtifactLock: lock}, nil
	}
	return e.regenerateChart(ctx, entry, destDir, deriveDistro)
}

// regenerateChart runs vendor → (distro) → render → discover.
func (e *Engine) regenerateChart(ctx context.Context, entry spec.Entry, destDir string,
	deriveDistro bool) (*Facts, error) {
	m := entry.Chart
	facts := &Facts{}

	// Vendor: pull the pinned tgz and extract it.
	e.notef(entry.Name, "vendor", "vendoring %s %s", m.Chart.Name, m.Chart.Version)
	tgz, sha, err := e.puller.Pull(ctx, m.Chart.Repo, m.Chart.Name, m.Chart.Version)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", entry.Name, err)
	}
	facts.VendorTgz = tgz
	vendor := filepath.Join(destDir, "vendor")
	if err := os.RemoveAll(vendor); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(vendor, 0o755); err != nil {
		return nil, err
	}
	if err := helmchart.Extract(tgz, vendor); err != nil {
		return nil, fmt.Errorf("%s: extract chart: %w", entry.Name, err)
	}
	if _, err := os.Stat(filepath.Join(vendor, m.Chart.Name, "Chart.yaml")); err != nil {
		return nil, fmt.Errorf("%s: extracted chart missing Chart.yaml", entry.Name)
	}
	e.notef(entry.Name, "vendor", "vendored %s (upstream tgz sha256 %s)", m.Chart.Name, sha)

	// Render with the entry's discovery values (which live in the entry
	// dir regardless of where the vendor tree materialized).
	var valuesFiles []string
	for _, f := range m.Discovery.ValuesFiles {
		path := filepath.Join(entry.Dir, f)
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("%s: values file %s not found under the entry directory", entry.Name, f)
		}
		valuesFiles = append(valuesFiles, path)
	}
	rendered, err := render.Render(ctx, render.Input{
		ChartDir:    filepath.Join(vendor, m.Chart.Name),
		ReleaseName: m.Chart.Name,
		Namespace:   m.Discovery.EffectiveNamespace(entry.Name),
		KubeVersion: m.Discovery.EffectiveKubeVersion(),
		APIVersions: m.Discovery.APIVersions,
		ValuesFiles: valuesFiles,
	})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", entry.Name, err)
	}
	facts.Rendered = rendered
	e.notef(entry.Name, "render", "rendered %d bytes", len(rendered))

	// Discover images (manifest extras merged with the generated sidecar)
	// and resolve each to a digest.
	appVersion, err := helmchart.AppVersion(vendor, m.Chart.Name)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", entry.Name, err)
	}
	if deriveDistro {
		if _, err := e.deriveDistribution(ctx, entry, vendor); err != nil {
			return nil, err
		}
	}
	sidecar, err := spec.LoadSidecar(entry.Dir)
	if err != nil {
		return nil, err
	}
	result, err := discover.Discover(discover.Input{
		Rendered:   rendered,
		AppVersion: appVersion,
		Extra:      append(append([]spec.ExtraImage{}, m.Images.Extra...), sidecar.Extra...),
		Exclude:    m.Images.Exclude,
		AllowEmpty: m.Images.AllowEmpty,
	})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", entry.Name, err)
	}
	facts.Excluded = result.Excluded
	for _, ref := range result.Excluded {
		e.notef(entry.Name, "discover", "excluded: %s", ref)
	}

	images, err := e.resolveImages(ctx, entry.Name, result.Images)
	if err != nil {
		return nil, err
	}
	facts.ImagesLock = &spec.ImagesLock{
		Chart: spec.LockChart{
			Name:              m.Chart.Name,
			Version:           m.Chart.Version,
			UpstreamTgzSha256: sha,
		},
		Images: images,
	}
	e.notef(entry.Name, "discover", "locked %d image(s)", len(images))
	return facts, nil
}

// resolveImages digest-pins every discovered reference, concurrently but
// deterministically (results sorted by source).
func (e *Engine) resolveImages(ctx context.Context, entryName string, sources []string) ([]spec.LockImage, error) {
	images := make([]spec.LockImage, len(sources))
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(e.workers)
	for i, source := range sources {
		g.Go(func() error {
			pullRef := e.rewrite(source)
			e.notef(entryName, "discover", "resolving %s", source)
			digest, err := e.reg.Digest(ctx, pullRef)
			if err != nil {
				return fmt.Errorf("%s: resolve digest for %s (via %s): %w", entryName, source, pullRef, err)
			}
			platforms, err := e.reg.Platforms(ctx, pullRef)
			if err != nil {
				return fmt.Errorf("%s: platforms of %s: %w", entryName, source, err)
			}
			target, err := e.imageTarget(source)
			if err != nil {
				return fmt.Errorf("%s: %w", entryName, err)
			}
			images[i] = spec.LockImage{Source: source, Digest: digest, Target: target, Platforms: platforms}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	sort.Slice(images, func(i, j int) bool { return images[i].Source < images[j].Source })
	return images, nil
}

// regenerateArtifact resolves an artifact entry's pinned version.
func (e *Engine) regenerateArtifact(ctx context.Context, entry spec.Entry) (*spec.ArtifactLock, error) {
	a := entry.Artifact.Artifact
	source := a.Ref + ":" + a.Version
	pullRef := e.rewrite(source)
	e.notef(entry.Name, "discover", "resolving %s", source)
	digest, err := e.reg.Digest(ctx, pullRef)
	if err != nil {
		return nil, fmt.Errorf("%s: resolve digest for %s (via %s): %w", entry.Name, source, pullRef, err)
	}
	platforms, err := e.reg.Platforms(ctx, pullRef)
	if err != nil {
		return nil, fmt.Errorf("%s: platforms of %s: %w", entry.Name, source, err)
	}
	return &spec.ArtifactLock{Artifact: spec.LockArtifact{
		Ref:       a.Ref,
		Version:   a.Version,
		Digest:    digest,
		Target:    fmt.Sprintf("%s/%s:%s", e.global.Registry.URL, e.artifactRepo(entry), a.Version),
		Platforms: platforms,
	}}, nil
}

// WriteFacts lands regenerated facts in the entry directory (the vendor
// tree is already in place when destDir was the entry dir).
func WriteFacts(entry spec.Entry, facts *Facts) error {
	if entry.Kind == spec.KindArtifact {
		return writeFile(entry.LockPath(), facts.ArtifactLock.Encode())
	}
	if err := os.MkdirAll(filepath.Dir(renderedPath(entry)), 0o755); err != nil {
		return err
	}
	if err := writeFile(renderedPath(entry), facts.Rendered); err != nil {
		return err
	}
	return writeFile(entry.LockPath(), facts.ImagesLock.Encode())
}

// writeFile writes atomically-enough for a git working tree.
func writeFile(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// ReadCommittedFacts loads the committed derived state for comparison with
// a regeneration. Missing files come back nil.
func ReadCommittedFacts(entry spec.Entry) (rendered []byte, lock []byte, err error) {
	if entry.Kind == spec.KindChart {
		rendered, err = readOptional(renderedPath(entry))
		if err != nil {
			return nil, nil, err
		}
	}
	lock, err = readOptional(entry.LockPath())
	if err != nil {
		return nil, nil, err
	}
	return rendered, lock, nil
}

// readOptional reads a file, mapping absence to nil.
func readOptional(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return raw, nil
}
