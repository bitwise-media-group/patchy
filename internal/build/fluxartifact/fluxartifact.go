// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package fluxartifact

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	gitignore "github.com/monochromegane/go-gitignore"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/partial"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// The Flux OCI artifact media types and the annotations Flux reads.
const (
	ConfigMediaType    = "application/vnd.cncf.flux.config.v1+json"
	ContentMediaType   = "application/vnd.cncf.flux.content.v1.tar+gzip"
	SourceAnnotation   = "org.opencontainers.image.source"
	RevisionAnnotation = "org.opencontainers.image.revision"
)

// alwaysIgnored is excluded from every artifact.
const alwaysIgnored = ".git/"

// Stage copies src into dst (which must not exist yet), dropping paths the
// gitignore-syntax patterns match plus .git/. Only regular files and
// directories are allowed: a symlink (or device, socket, pipe) is an error,
// since what it points at would escape review.
func Stage(src, dst string, ignore []string) error {
	src, err := filepath.Abs(src)
	if err != nil {
		return err
	}
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("manifests path: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("manifests path %s is not a directory", src)
	}
	matcher := gitignore.NewGitIgnoreFromReader(src,
		strings.NewReader(strings.Join(append([]string{alwaysIgnored}, ignore...), "\n")))
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, 0o755)
		}
		if matcher.Match(path, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			return fmt.Errorf("%s is a symlink: a manifests tree may hold only regular files and directories", rel)
		case d.IsDir():
			return os.MkdirAll(target, 0o755)
		case d.Type().IsRegular():
			return copyFile(path, target)
		default:
			return fmt.Errorf("%s is not a regular file or directory", rel)
		}
	})
}

// copyFile copies one regular file, normalizing its mode to 0644, or 0755
// when any execute bit is set.
func copyFile(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_EXCL, normalMode(info.Mode()))
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// normalMode keeps only the execute decision.
func normalMode(m fs.FileMode) fs.FileMode {
	if m&0o111 != 0 {
		return 0o755
	}
	return 0o644
}

// epoch is the fixed timestamp every tar entry carries.
var epoch = time.Unix(0, 0).UTC()

// Archive writes the tree at dir as a deterministic tar.gz: entries in
// lexical order with slash-separated relative paths, modes 0644/0755,
// times at the epoch, owners 0, and a gzip header with no name or mtime.
func Archive(dir string) ([]byte, error) {
	var buf bytes.Buffer
	gz, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	tw := tar.NewWriter(gz)
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil || rel == "." {
			return err
		}
		name := filepath.ToSlash(rel)
		hdr := &tar.Header{ModTime: epoch, Format: tar.FormatPAX}
		switch {
		case d.IsDir():
			hdr.Typeflag, hdr.Name, hdr.Mode = tar.TypeDir, name+"/", 0o755
			return tw.WriteHeader(hdr)
		case d.Type().IsRegular():
			info, err := d.Info()
			if err != nil {
				return err
			}
			hdr.Typeflag, hdr.Name, hdr.Mode, hdr.Size = tar.TypeReg, name, int64(normalMode(info.Mode())), info.Size()
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer func() { _ = f.Close() }()
			_, err = io.Copy(tw, f)
			return err
		default:
			return fmt.Errorf("%s is not a regular file or directory", rel)
		}
	})
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Package archives dir and wraps it as a Flux OCI artifact carrying the
// given manifest annotations (empty values are dropped).
func Package(dir string, annotations map[string]string) (v1.Image, error) {
	content, err := Archive(dir)
	if err != nil {
		return nil, err
	}
	config := []byte("{}")
	layer := static.NewLayer(content, ContentMediaType)
	layerDigest, err := layer.Digest()
	if err != nil {
		return nil, err
	}
	m := v1.Manifest{
		SchemaVersion: 2,
		MediaType:     types.OCIManifestSchema1,
		Config:        v1.Descriptor{MediaType: ConfigMediaType, Size: int64(len(config)), Digest: sha256Hash(config)},
		Layers:        []v1.Descriptor{{MediaType: ContentMediaType, Size: int64(len(content)), Digest: layerDigest}},
	}
	for k, v := range annotations {
		if v == "" {
			continue
		}
		if m.Annotations == nil {
			m.Annotations = map[string]string{}
		}
		m.Annotations[k] = v
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return partial.CompressedToImage(&artifact{
		manifest: raw,
		config:   config,
		blobs: map[v1.Hash]partial.CompressedLayer{
			layerDigest:     layer,
			m.Config.Digest: static.NewLayer(config, ConfigMediaType),
		},
	})
}

// sha256Hash digests raw bytes.
func sha256Hash(b []byte) v1.Hash {
	sum := sha256.Sum256(b)
	return v1.Hash{Algorithm: "sha256", Hex: hex.EncodeToString(sum[:])}
}

// artifact is the minimal compressed image core: a fixed manifest, a raw
// {} config, and its blobs by digest.
type artifact struct {
	manifest []byte
	config   []byte
	blobs    map[v1.Hash]partial.CompressedLayer
}

// RawConfigFile implements partial.CompressedImageCore.
func (a *artifact) RawConfigFile() ([]byte, error) { return a.config, nil }

// MediaType implements partial.CompressedImageCore.
func (a *artifact) MediaType() (types.MediaType, error) { return types.OCIManifestSchema1, nil }

// RawManifest implements partial.CompressedImageCore.
func (a *artifact) RawManifest() ([]byte, error) { return a.manifest, nil }

// LayerByDigest implements partial.CompressedImageCore.
func (a *artifact) LayerByDigest(h v1.Hash) (partial.CompressedLayer, error) {
	if l, ok := a.blobs[h]; ok {
		return l, nil
	}
	return nil, fmt.Errorf("blob %s not in artifact", h)
}
