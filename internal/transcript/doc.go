// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package transcript is the agent conversation contract: the harness-neutral
// turn vocabulary every agent CLI's stream is projected onto, the prefixed
// JSONL line format that carries turns out of the agent pod on stdout, and the
// gzipped ConfigMap the owning controller persists them into.
//
// Turns share stdout with the envelope's stage results but use their own
// prefix, so the two streams stay separate: a run emits hundreds of turns and
// one result, and the controller's result path should not have to walk the
// turns to find the result.
//
// The in-pod recorder and the status server both round-trip through this
// package, so the capture and playback shapes cannot drift.
package transcript
