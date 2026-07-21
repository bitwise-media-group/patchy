// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package labels renders the human-facing security-* label vocabulary the
// issue projection stamps on tracking issues: source, advisories, the
// finding's phase, severity, priority, and the investigation's verdict. The
// Finding custom resource is the state machine — these labels are a one-way
// projection for humans and issue searches, never parsed back into state.
//
// GitHub caps label names at 50 characters, so Render truncates; anything a
// label abbreviates lives in full on the Finding.
package labels
