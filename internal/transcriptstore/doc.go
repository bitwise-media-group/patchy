// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package transcriptstore persists an agent run's conversation and reads it
// back: one gzipped-JSONL ConfigMap per run, owned by the run that produced
// it. The owner reference is the whole retention design — a run is owned by
// its Finding, so a Finding deleted on TTL cascades to the run and then to its
// transcript, and no expiry logic of its own is needed.
//
// It is deliberately separate from internal/transcript. That package is the
// vocabulary and the wire format and runs inside the agent pod, which holds no
// Kubernetes credential and must not link a client; this one is the
// controller and status-server half.
package transcriptstore
