// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package cli

import (
	"fmt"
	"os"
	"testing"
)

// TestMain isolates the layered patchy config: tests see a temp $HOME and
// system directory, so a developer's own ~/.config/patchy or /etc/patchy
// never leaks into an assertion. The working directory stays the real one
// (tests t.Chdir into temp dirs).
func TestMain(m *testing.M) {
	base, err := os.MkdirTemp("", "patchy-cli-config-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	configOptions.Home = base + "/home"
	configOptions.SystemDir = base + "/etc"
	code := m.Run()
	_ = os.RemoveAll(base)
	os.Exit(code)
}
