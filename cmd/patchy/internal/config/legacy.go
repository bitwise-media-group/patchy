// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// legacyCLIExtensions are the formats the old .patchy.<ext> accepted.
var legacyCLIExtensions = []string{"yaml", "yml", "json"}

// legacyWarnings flags a .patchy.<ext> left in the working directory: it is
// no longer read, and silently ignoring it would make its values vanish.
func legacyWarnings(o Options) []string {
	var out []string
	for _, ext := range legacyCLIExtensions {
		name := ".patchy." + ext
		if info, err := os.Stat(filepath.Join(o.Workdir, name)); err == nil && !info.IsDir() {
			out = append(out, fmt.Sprintf("%s is no longer read; move its contents to patchy.yaml "+
				"(or .patchy/config.yaml, .mise/patchy.yaml, patchy/config.yaml, mise/patchy.yaml — "+
				"yaml, yml, json or toml)", name))
		}
	}
	return out
}

// LegacyMirrorConfig is the removed mirror store config file name.
const LegacyMirrorConfig = "mirror.yaml"

// ErrLegacyMirrorConfig reports a mirror.yaml at the mirror root.
var ErrLegacyMirrorConfig = errors.New(LegacyMirrorConfig + " is no longer read")

// CheckMirrorRoot fails when the mirror root still carries a mirror.yaml,
// naming the migration: its blocks move into the patchy config.
func CheckMirrorRoot(root string) error {
	path := filepath.Join(root, LegacyMirrorConfig)
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		return nil
	}
	return fmt.Errorf("%w (found %s): move its blocks into patchy.yaml in the working directory — "+
		"registries, signing and scan at the top level, update and sourceRegistryRewrites under mirror:, "+
		"scan.allowlistMaxDays/allowlistNewDays to scan.allowlist.maxDays/newDays, and drop apiVersion/kind — "+
		"then delete %s", ErrLegacyMirrorConfig, path, LegacyMirrorConfig)
}
