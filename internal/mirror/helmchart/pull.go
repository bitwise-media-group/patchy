// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package helmchart

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/bitwise-media-group/patchy/internal/mirror/imageref"
	"github.com/bitwise-media-group/patchy/internal/mirror/ocireg"
)

// ChartContentLayerMediaType is the OCI layer holding a chart's tgz.
const ChartContentLayerMediaType = "application/vnd.cncf.helm.chart.content.v1.tar+gzip"

// ChartConfigMediaType marks an OCI artifact as a helm chart.
const ChartConfigMediaType = "application/vnd.cncf.helm.config.v1+json"

// Puller fetches chart archives from upstream repositories.
type Puller struct {
	// Registry performs OCI pulls.
	Registry *ocireg.Client
	// HTTP performs helm-repository pulls (nil: http.DefaultClient).
	HTTP *http.Client
	// Rewrites reroutes OCI pulls per registry host.
	Rewrites map[string]string
}

// httpClient resolves the HTTP default.
func (p *Puller) httpClient() *http.Client {
	if p.HTTP != nil {
		return p.HTTP
	}
	return http.DefaultClient
}

// Pull fetches the pinned chart tgz and returns its bytes and sha256.
// repo is the manifest's chart.repo: oci://<registry path> or an https://
// helm repository URL.
func (p *Puller) Pull(ctx context.Context, repo, name, version string) ([]byte, string, error) {
	var (
		data []byte
		err  error
	)
	switch {
	case strings.HasPrefix(repo, "oci://"):
		ref := imageref.Rewrite(strings.TrimPrefix(repo, "oci://")+"/"+name, p.Rewrites) + ":" + version
		data, err = p.Registry.LayerBytes(ctx, ref, ChartContentLayerMediaType)
	case strings.HasPrefix(repo, "https://"), strings.HasPrefix(repo, "http://"):
		data, err = p.pullFromRepo(ctx, repo, name, version)
	default:
		return nil, "", fmt.Errorf("chart repo %q is neither oci:// nor https://", repo)
	}
	if err != nil {
		return nil, "", fmt.Errorf("pull chart %s %s: %w", name, version, err)
	}
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:]), nil
}

// repoIndex is the subset of a helm repository index the puller reads.
type repoIndex struct {
	Entries map[string][]struct {
		Version string   `yaml:"version"`
		URLs    []string `yaml:"urls"`
	} `yaml:"entries"`
}

// pullFromRepo resolves version through the repository's index.yaml and
// downloads the archive (whose URL may be relative to the repo).
func (p *Puller) pullFromRepo(ctx context.Context, repo, name, version string) ([]byte, error) {
	indexURL := strings.TrimSuffix(repo, "/") + "/index.yaml"
	raw, err := p.get(ctx, indexURL)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", indexURL, err)
	}
	var idx repoIndex
	if err := yaml.Unmarshal(raw, &idx); err != nil {
		return nil, fmt.Errorf("parse %s: %w", indexURL, err)
	}
	for _, e := range idx.Entries[name] {
		if e.Version != version && strings.TrimPrefix(e.Version, "v") != strings.TrimPrefix(version, "v") {
			continue
		}
		if len(e.URLs) == 0 {
			return nil, fmt.Errorf("index entry %s %s has no urls", name, version)
		}
		u, err := resolveURL(repo, e.URLs[0])
		if err != nil {
			return nil, err
		}
		return p.get(ctx, u)
	}
	return nil, fmt.Errorf("repository index has no %s %s", name, version)
}

// Versions lists a helm repository's versions of one chart, via index.yaml.
func (p *Puller) Versions(ctx context.Context, repo, name string) ([]string, error) {
	indexURL := strings.TrimSuffix(repo, "/") + "/index.yaml"
	raw, err := p.get(ctx, indexURL)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", indexURL, err)
	}
	var idx repoIndex
	if err := yaml.Unmarshal(raw, &idx); err != nil {
		return nil, fmt.Errorf("parse %s: %w", indexURL, err)
	}
	var versions []string
	for _, e := range idx.Entries[name] {
		versions = append(versions, e.Version)
	}
	return versions, nil
}

// resolveURL resolves a possibly-relative archive URL against the repo URL.
func resolveURL(repo, ref string) (string, error) {
	base, err := url.Parse(strings.TrimSuffix(repo, "/") + "/")
	if err != nil {
		return "", fmt.Errorf("parse repo url %q: %w", repo, err)
	}
	u, err := base.Parse(ref)
	if err != nil {
		return "", fmt.Errorf("parse archive url %q: %w", ref, err)
	}
	return u.String(), nil
}

// get performs one GET returning the body.
func (p *Puller) get(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	return io.ReadAll(resp.Body)
}
