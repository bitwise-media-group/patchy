// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package evaluation runs the remote-evaluation engine: the gate reconciler
// expands each immutable Evaluation into its EvaluationUnit children, the
// unit reconciler schedules them through the sandboxed agent-Job machinery
// with bounded concurrency and collects each pod's EVOLVE-EVENT stream onto
// unit statuses (full-fidelity entries into per-unit ConfigMaps), and the
// TTL loop deletes finished evaluations after their retention. An
// Evaluation's phase is derived from its children's. This package is the
// single writer for both kinds, so no transition table is involved.
package evaluation
