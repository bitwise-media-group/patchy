// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package ansi strips terminal escape sequences from captured agent output.
//
// It is its own package because both the process runner (stripping collected
// stdout) and the transcript recorder (stripping a decoded stream event's
// text) need it, and the recorder runs in packages that must not depend on the
// runner's process-execution and telemetry machinery.
package ansi
