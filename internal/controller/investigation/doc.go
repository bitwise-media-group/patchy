// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package investigation is the investigation-controller's engine: it turns
// Enhanced findings into analysis agent runs and routes their verdicts.
//
// The gate reconciler admits findings once the accumulation window is closed
// and the minimum age has passed, resolves the covering Forge, materializes
// the SHA-pinned Repository artifact, and creates one immutable
// Investigation child per attempt (the create is the lease). The
// investigation reconciler grants bounded-concurrency slots in
// severity-priority order (internal/schedule), launches the agent Job
// (credential-less artifact flow), and — when the Job completes — applies
// the envelope's investigation event: child status, the Finding's summary
// and scheduling priority, and the verdict routing
// (Queued / AwaitingApproval / Dismissed / HandedOff / Failed, with retry
// reverts to Enhanced while attempts remain).
package investigation
