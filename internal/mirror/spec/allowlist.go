// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package spec

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// AllowlistFile is an entry's allowlist path relative to its directory.
const AllowlistFile = "security/allowlist.yaml"

// Allowlist is an entry's security/allowlist.yaml: accepted scan findings,
// each with a statement and a lint-enforced expiry.
type Allowlist struct {
	Vulnerabilities []AllowlistEntry `yaml:"vulnerabilities"`
}

// AllowlistEntry accepts one finding until it expires.
type AllowlistEntry struct {
	// ID is the finding identifier (CVE, GHSA, GO-...); matching also
	// covers the finding's aliases, so an entry keeps working across
	// scanners reporting under different id families.
	ID string `yaml:"id"`
	// Statement records the machine facts: package, installed version,
	// fixed-in version.
	Statement string `yaml:"statement"`
	// ExpiredAt (YYYY-MM-DD) forces re-review; entries never outlive it.
	ExpiredAt string `yaml:"expired_at"`
	// Notes preserves human analysis (reachability, exposure) verbatim
	// across regenerations.
	Notes string `yaml:"notes,omitempty"`
}

// LoadAllowlist reads an entry's allowlist. A missing file is an empty
// allowlist, not an error.
func LoadAllowlist(entryDir string) (*Allowlist, error) {
	path := filepath.Join(entryDir, AllowlistFile)
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &Allowlist{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var a Allowlist
	if err := decodeStrict(raw, &a); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &a, nil
}
