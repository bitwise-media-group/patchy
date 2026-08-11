// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package allowlist

import (
	"os"
	"path/filepath"
)

// mkdirWrite creates dir and writes name inside it.
func mkdirWrite(dir, name string, data []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name), data, 0o644)
}
