// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package build

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/bitwise-media-group/patchy/internal/mirror/ocireg"
)

// built is one build output: a single image or a multi-platform index.
// Exactly one of image and index is set.
type built struct {
	image  v1.Image
	index  v1.ImageIndex
	digest v1.Hash
}

// write pushes the build to ref.
func (b built) write(ctx context.Context, reg *ocireg.Client, ref string) error {
	if b.index != nil {
		return reg.WriteIndex(ctx, ref, b.index)
	}
	return reg.Write(ctx, ref, b.image)
}

// loadBuilt loads a build's output; a seam so engine tests can publish an
// in-memory copy of what the fake builder wrote (loadLayout itself is
// tested on its own).
var loadBuilt = loadLayout

// loadLayout opens a buildx OCI layout export: index.json holds one root
// descriptor, an image index (multi-platform, or single-platform with
// attestations) or a lone image manifest.
func loadLayout(dir string) (built, error) {
	p, err := layout.FromPath(dir)
	if err != nil {
		return built{}, fmt.Errorf("open build output: %w", err)
	}
	idx, err := p.ImageIndex()
	if err != nil {
		return built{}, fmt.Errorf("read build output: %w", err)
	}
	m, err := idx.IndexManifest()
	if err != nil {
		return built{}, fmt.Errorf("read build output: %w", err)
	}
	if len(m.Manifests) != 1 {
		return built{}, fmt.Errorf("build output holds %d root manifests, want exactly one", len(m.Manifests))
	}
	d := m.Manifests[0]
	switch {
	case d.MediaType.IsIndex():
		child, err := idx.ImageIndex(d.Digest)
		if err != nil {
			return built{}, err
		}
		return built{index: child, digest: d.Digest}, nil
	case d.MediaType.IsImage():
		child, err := idx.Image(d.Digest)
		if err != nil {
			return built{}, err
		}
		return built{image: child, digest: d.Digest}, nil
	default:
		return built{}, fmt.Errorf("build output root has unsupported media type %s", d.MediaType)
	}
}

// platformImage is one runnable image inside a build.
type platformImage struct {
	platform string
	img      v1.Image
}

// scanRef names the throwaway tag a docker archive carries for scanners.
var scanRef = name.MustParseReference("patchy.local/build:scan")

// saveArchive writes the image as a docker-archive tarball.
func (p platformImage) saveArchive(path string) error {
	if err := tarball.WriteToFile(path, scanRef, p.img); err != nil {
		return fmt.Errorf("export %s for scanning: %w", p.platform, err)
	}
	return nil
}

// platformImages lists the runnable images to scan, skipping attestation
// manifests (platform unknown/unknown, or a vnd.docker.reference.type
// annotation).
func (b built) platformImages() ([]platformImage, error) {
	if b.image != nil {
		cfg, err := b.image.ConfigFile()
		if err != nil {
			return nil, err
		}
		platform := "unknown"
		if p := cfg.Platform(); p != nil {
			platform = p.String()
		}
		return []platformImage{{platform: platform, img: b.image}}, nil
	}
	m, err := b.index.IndexManifest()
	if err != nil {
		return nil, err
	}
	var out []platformImage
	for _, d := range m.Manifests {
		if d.Annotations["vnd.docker.reference.type"] != "" || !d.MediaType.IsImage() ||
			(d.Platform != nil && d.Platform.OS == "unknown") {
			continue
		}
		img, err := b.index.Image(d.Digest)
		if err != nil {
			return nil, err
		}
		platform := "unknown"
		if d.Platform != nil {
			platform = d.Platform.String()
		}
		out = append(out, platformImage{platform: platform, img: img})
	}
	if len(out) == 0 {
		return nil, errors.New("the build produced no platform images to scan")
	}
	return out, nil
}
