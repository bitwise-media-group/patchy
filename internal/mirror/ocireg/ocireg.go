// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package ocireg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

// Client performs registry operations with one keychain.
type Client struct {
	keychain authn.Keychain
}

// New builds a client. A nil keychain falls back to the ambient default
// (docker config credentials).
func New(keychain authn.Keychain) *Client {
	if keychain == nil {
		keychain = authn.DefaultKeychain
	}
	return &Client{keychain: keychain}
}

// options builds the per-call crane options.
func (c *Client) options(ctx context.Context) []crane.Option {
	return []crane.Option{
		crane.WithContext(ctx),
		crane.WithAuthFromKeychain(c.keychain),
	}
}

// Digest resolves a reference to its manifest digest.
func (c *Client) Digest(ctx context.Context, ref string) (string, error) {
	d, err := crane.Digest(ref, c.options(ctx)...)
	if err != nil {
		return "", fmt.Errorf("resolve digest of %s: %w", ref, err)
	}
	return d, nil
}

// Exists reports whether ref resolves, returning its digest when it does.
// A missing manifest or repository is (false, nil); other failures are
// errors.
func (c *Client) Exists(ctx context.Context, ref string) (string, bool, error) {
	d, err := crane.Digest(ref, c.options(ctx)...)
	if err != nil {
		if isNotFound(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("probe %s: %w", ref, err)
	}
	return d, true, nil
}

// isNotFound recognizes registry 404s (missing manifest, tag or repository).
func isNotFound(err error) bool {
	if te, ok := errors.AsType[*transport.Error](err); ok {
		return te.StatusCode == http.StatusNotFound
	}
	return false
}

// Tags lists a repository's tags.
func (c *Client) Tags(ctx context.Context, repo string) ([]string, error) {
	tags, err := crane.ListTags(repo, c.options(ctx)...)
	if err != nil {
		return nil, fmt.Errorf("list tags of %s: %w", repo, err)
	}
	return tags, nil
}

// Platforms lists the os/arch pairs of a manifest list, sorted and
// deduplicated, excluding "unknown" (attestation) entries. A
// single-platform image yields an empty list.
func (c *Client) Platforms(ctx context.Context, ref string) ([]string, error) {
	raw, err := crane.Manifest(ref, c.options(ctx)...)
	if err != nil {
		return nil, fmt.Errorf("fetch manifest of %s: %w", ref, err)
	}
	var idx struct {
		Manifests []struct {
			Platform *v1.Platform `json:"platform"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(raw, &idx); err != nil {
		return nil, fmt.Errorf("parse manifest of %s: %w", ref, err)
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range idx.Manifests {
		if m.Platform == nil || m.Platform.OS == "unknown" || m.Platform.OS == "" {
			continue
		}
		p := m.Platform.OS + "/" + m.Platform.Architecture
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out, nil
}

// Created returns an image's config creation timestamp. ok=false means the
// config carries none (or the artifact has no image config at all).
func (c *Client) Created(ctx context.Context, ref string) (time.Time, bool, error) {
	raw, err := crane.Config(ref, c.options(ctx)...)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("fetch config of %s: %w", ref, err)
	}
	var cfg struct {
		Created *time.Time `json:"created"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return time.Time{}, false, nil
	}
	if cfg.Created == nil || cfg.Created.IsZero() {
		return time.Time{}, false, nil
	}
	return *cfg.Created, true, nil
}

// ConfigMediaType returns the manifest's config media type, used to detect
// helm charts (application/vnd.cncf.helm.config.v1+json) among OCI
// artifacts.
func (c *Client) ConfigMediaType(ctx context.Context, ref string) (string, error) {
	raw, err := crane.Manifest(ref, c.options(ctx)...)
	if err != nil {
		return "", fmt.Errorf("fetch manifest of %s: %w", ref, err)
	}
	var m struct {
		Config struct {
			MediaType string `json:"mediaType"`
		} `json:"config"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", fmt.Errorf("parse manifest of %s: %w", ref, err)
	}
	return m.Config.MediaType, nil
}

// Copy replicates src to dst byte-preservingly (manifest lists keep their
// original bytes and children), like crane cp.
func (c *Client) Copy(ctx context.Context, src, dst string) error {
	if err := crane.Copy(src, dst, c.options(ctx)...); err != nil {
		return fmt.Errorf("copy %s to %s: %w", src, dst, err)
	}
	return nil
}

// SaveTarball writes ref as a docker-archive tarball at path, for scanners
// that consume local image archives.
func (c *Client) SaveTarball(ctx context.Context, ref, path string) error {
	img, err := crane.Pull(ref, c.options(ctx)...)
	if err != nil {
		return fmt.Errorf("pull %s: %w", ref, err)
	}
	if err := crane.Save(img, ref, path); err != nil {
		return fmt.Errorf("save %s: %w", ref, err)
	}
	return nil
}

// Export streams ref's flattened filesystem (all layers applied) as a tar
// stream into w, like crane export.
func (c *Client) Export(ctx context.Context, ref string, w io.Writer) error {
	img, err := crane.Pull(ref, c.options(ctx)...)
	if err != nil {
		return fmt.Errorf("pull %s: %w", ref, err)
	}
	if err := crane.Export(img, w); err != nil {
		return fmt.Errorf("export %s: %w", ref, err)
	}
	return nil
}

// LayerBytes fetches one layer of ref by media-type suffix match, for
// single-layer content artifacts (helm chart tgz layers). Exactly one layer
// must match.
func (c *Client) LayerBytes(ctx context.Context, ref, mediaType string) ([]byte, error) {
	r, err := name.ParseReference(ref)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", ref, err)
	}
	img, err := remote.Image(
		r,
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(c.keychain),
	)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", ref, err)
	}
	layers, err := img.Layers()
	if err != nil {
		return nil, fmt.Errorf("layers of %s: %w", ref, err)
	}
	var match v1.Layer
	for _, l := range layers {
		mt, err := l.MediaType()
		if err != nil {
			return nil, err
		}
		if string(mt) == mediaType {
			if match != nil {
				return nil, fmt.Errorf("%s has multiple %s layers", ref, mediaType)
			}
			match = l
		}
	}
	if match == nil {
		return nil, fmt.Errorf("%s has no %s layer", ref, mediaType)
	}
	rc, err := match.Compressed()
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("read layer of %s: %w", ref, err)
	}
	return data, nil
}
