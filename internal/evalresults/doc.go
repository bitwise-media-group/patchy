// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package evalresults persists an evaluation unit's full-fidelity result
// entry — the opaque JSON document the in-pod client emitted — in a per-unit
// ConfigMap, gzip-compressed under the 1 MiB object cap. The bounded summary
// lives on the EvaluationUnit status; this store carries the everything-else
// the submitting client reassembles into its local results file. The
// ConfigMap is owned by its unit, which is owned by its Evaluation: TTL
// deletion cascades, so a result is retained exactly as long as the
// submission it belongs to.
package evalresults
