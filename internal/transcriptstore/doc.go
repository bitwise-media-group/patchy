// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package transcriptstore persists an agent run's conversation and reads it
// back: one gzipped-JSONL ConfigMap per run, owned by the run that produced
// it. The owner reference is the whole retention mechanism: a run is owned by
// its Finding, so a Finding deleted on TTL cascades to the run and then to its
// transcript, and this package needs no expiry logic.
//
// It is separate from internal/transcript because that package (the
// vocabulary and the wire format) runs inside the agent pod, which holds no
// Kubernetes credential and must not link a client. This package is the
// controller and status-server half.
package transcriptstore
