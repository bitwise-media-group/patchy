// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package jobs creates and observes the ephemeral Kubernetes Jobs that run
// agent-runner: one Job per Investigation/Remediation attempt,
// deterministically named, labelled by kind/owner/finding (the two job
// controllers share the agents namespace and filter on the kind label), and
// garbage collected via TTL plus an owner-referenced per-Job Secret.
//
// The Job shape carries the isolation model. A brokered (claude, codex) pod
// holds no credential of any kind: its model traffic goes through the egress
// broker, authenticated by an audience-bound projected ServiceAccount token.
// Only a generic, non-brokered runner receives a model credential, through a
// SecretKeyRef. The init container fetches the repository as a
// digest-verified tarball from source-controller's artifact server and
// synthesizes the local git base; the per-Job Secret holds only the handoff
// markdown. Results come back on the agent container's stdout as envelope
// events, read once at Job completion.
package jobs
