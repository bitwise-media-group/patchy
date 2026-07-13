// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package envelope is the versioned event contract between agent-runner and
// the remediation-controller: one JSON event per line on the runner's
// stdout, prefixed so it survives interleaving with any other output. The
// controller follows the pod log live (issue updates land between the
// classification and remediation stages of one pod) and re-reads the full
// log at Job completion as the fallback — events are self-contained and
// idempotent to re-apply.
package envelope
