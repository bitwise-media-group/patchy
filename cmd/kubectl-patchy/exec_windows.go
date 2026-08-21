// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

//go:build windows

package main

// run executes patchy as a child process — windows has no exec — passing
// stdio through and propagating the exit code.
func run(path string, args []string) int {
	return passthrough(path, args)
}
