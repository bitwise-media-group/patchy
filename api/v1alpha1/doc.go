// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package v1alpha1 defines the patchy.bitwisemedia.uk/v1alpha1 API: the
// custom resources that carry finding state through the pipeline.
//
// The kinds fall into three groups:
//
//   - Edge configuration: Integration (an external system — scanner ingestion
//     and/or tracking projection, e.g. a GitHub App handling code-scanning
//     alerts in and issue tracking out) and Forge (repository credentials for
//     cloning, pushing, and pull requests, selected by host/org/repo filters).
//   - The pipeline: Finding is the authoritative state machine (phases and
//     legal transitions live in transitions.go); Investigation and Remediation
//     are one-per-attempt immutable children recording each agent run;
//     Repository is the SHA-pinned clone artifact source-controller serves to
//     agent jobs as a tarball.
//   - Statistics: FindingRollup objects — one per scope value (total,
//     repository, harness, model) — accumulate all-time counters that survive
//     the TTL deletion of completed Findings.
//   - Remote evaluations: Evaluation is one submission of coding-agent skill
//     evaluations from an external client (evolve); EvaluationUnit is its
//     one-per-unit immutable child recording each sandboxed run. Both expire
//     on a TTL; their phases are local enums, not part of the Finding state
//     machine.
//
// Structural-schema rules shape the types: no floats (confidence and cost are
// validated decimal strings; rollup sums are int64 micro-USD; durations are
// int64 milliseconds), bounded sizes everywhere (etcd caps objects at about
// 1.5MiB), and agent changesets never appear in any resource.
package v1alpha1
