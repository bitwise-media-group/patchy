// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package runner executes CommandSpecs. It is the only package that touches
// os/exec, so every caller can be tested against a fake.
//
// Agent CLIs spawn children, so cancellation kills the whole process group —
// killing only the parent leaks grandchildren that hold the stdout pipe open.
package runner
