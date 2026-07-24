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
// The URL is validated before it reaches a shell-adjacent command: these come
// from CR status written by a controller, but "it came from the cluster" is not
// the same as "it is safe to hand to a launcher", and an http(s) check costs
// nothing.
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
	// Deliberately not Wait: the launcher may block for as long as the browser
	// lives, and the CLI has nothing left to do.
	return nil
}
