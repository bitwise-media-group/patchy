// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

//go:build !windows

package main

import (
	"fmt"
	"os"
	"syscall"
)

// run replaces the shim process with patchy, so signals, the tty, and the
// exit code all belong to the real CLI. It falls back to a child process
// only if exec itself fails.
func run(path string, args []string) int {
	argv := append([]string{path}, args...)
	if err := syscall.Exec(path, argv, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "kubectl-patchy: exec %s: %v\n", path, err)
		return passthrough(path, args)
	}
	return 0
}
