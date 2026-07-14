// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package ghpush applies an agent's changeset to GitHub through the Git Data
// API. The agent works in a network-isolated pod and hands its commit back
// as structured file contents (envelope.Changeset); the controller replays
// that changeset as blob → tree → commit → ref calls with a short-lived,
// single-repository write token — the only place a push credential exists.
// No git binary and no clone are involved, so the push costs the same on a
// ten-commit repository as on one with a decade of history.
package ghpush
