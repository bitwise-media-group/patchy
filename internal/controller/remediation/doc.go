// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package remediation is the remediation-controller's engine — three
// reconcilers over the Finding/Remediation kinds:
//
// The spawner is the queue-admission writer: it turns approvals and
// revivals into Queued findings and materializes one immutable, Pending
// Remediation child per attempt, stamped with its scheduling priority
// (internal/priority). The remediation reconciler grants bounded slots to
// pending remediations in priority order (internal/schedule), launches the
// coding agent in an ephemeral Kubernetes Job, and applies the result when
// the Job completes: on success it replays the agent's changeset through
// the forge write seam (internal/forge + internal/ghpush — the only holder
// of a forge write credential) and opens the pull request; on failure it
// re-queues or exhausts the finding. Merging stays human; the merge webhook
// (integration-controller) completes the finding.
//
// The binary also hosts internal/controller/rollup — the all-time
// statistics aggregation and the finding TTL.
package remediation
