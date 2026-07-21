// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package envelope is the versioned event contract between agent-runner and
// the job controllers: one JSON event per line on the runner's stdout,
// prefixed so it survives interleaving with any other output. The owning
// controller reads the full log once at Job completion — events are
// self-contained and idempotent to re-apply.
package envelope
