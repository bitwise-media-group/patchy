// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

//go:build windows

package runner

import (
	"os/exec"
	"strconv"
)

// configureProcessTreeKill makes cancellation take down the runner CLI's whole
// process tree, not just the direct child. Windows has no POSIX process
// groups; taskkill /T walks the tree and /F force-kills it.
func configureProcessTreeKill(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		return exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
	}
}
