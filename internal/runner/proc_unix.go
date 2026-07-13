// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

//go:build unix

package runner

import (
	"os/exec"
	"syscall"
)

// configureProcessTreeKill makes cancellation take down the runner CLI's whole
// process tree, not just the direct child: the child starts in its own process
// group and Cancel SIGKILLs that group (negative pid).
func configureProcessTreeKill(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
