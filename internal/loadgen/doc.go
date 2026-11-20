// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package loadgen generates deterministic synthetic Finding, FindingRollup,
// and Investigation resources for benchmarks and load tests. The same (index,
// Opts) pair always yields byte-identical objects, so benchmark datasets are
// reproducible across runs and machines. A scanner-side alert built by
// SourceFinding folds into the Finding built by the same index, because the
// label hash follows the accumulation-key recipe the ingester persists.
//
// TEST-ONLY: import this package only from _test.go files, never from product
// code. Its objects fabricate status fields that production components own
// exclusively.
package loadgen
