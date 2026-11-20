// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package browser opens a URL in the user's default browser.
package browser

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// Open launches url in the default browser.
//
// The URL must be http or https before it reaches a shell-adjacent command.
// These URLs come from CR status written by a controller, but coming from the
// cluster does not make a value safe to hand to a launcher.
func Open(url string) error {
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return fmt.Errorf("refusing to open %q: not an http(s) URL", url)
	}

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("open %s: %w", url, err)
	}
	// No Wait: the launcher may block for as long as the browser lives, and
	// the CLI has nothing left to do.
	return nil
}
