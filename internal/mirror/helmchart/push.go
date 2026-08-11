// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package helmchart

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/google/go-containerregistry/pkg/name"
	"helm.sh/helm/v4/pkg/registry"
)

// Push publishes a chart tgz to ref (repo:version) the way helm push does:
// the byte-identical archive as the content layer, the chart metadata as
// the config blob. Returns the pushed manifest digest.
//
// Push never checks for an existing tag — the caller owns the
// never-replace rule (an existing tag must be skipped, not overwritten).
func Push(tgz []byte, ref string) (string, error) {
	opts := []registry.ClientOption{registry.ClientOptWriter(io.Discard)}
	parsed, err := name.ParseReference(ref)
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", ref, err)
	}
	if parsed.Context().Scheme() == "http" {
		opts = append(opts, registry.ClientOptPlainHTTP())
	}
	// Read the docker keychain (what `crane auth login` / `docker login`
	// write, and what every other registry operation here authenticates
	// with) instead of helm's own registry.json, so one login covers the
	// whole pipeline.
	if creds := dockerConfigPath(); creds != "" {
		opts = append(opts, registry.ClientOptCredentialsFile(creds))
	}
	client, err := registry.NewClient(opts...)
	if err != nil {
		return "", fmt.Errorf("build registry client: %w", err)
	}
	result, err := client.Push(tgz, ref)
	if err != nil {
		return "", fmt.Errorf("push chart to %s: %w", ref, err)
	}
	return result.Manifest.Digest, nil
}

// dockerConfigPath locates the docker config.json, honoring DOCKER_CONFIG;
// empty when none exists (anonymous registries, tests).
func dockerConfigPath() string {
	dir := os.Getenv("DOCKER_CONFIG")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".docker")
	}
	path := filepath.Join(dir, "config.json")
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		return ""
	}
	return path
}
