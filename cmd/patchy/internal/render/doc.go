// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package render turns one patchy resource into a printer document.
//
// It is organised by noun — one file per kind — because a kind is what changes
// together: add a field to InvestigationStatus and exactly one file here needs
// editing. The verb commands stay noun-agnostic, which is what lets `patchy get`
// serve every kind through the resource registry without a per-kind branch.
//
// Each kind offers two depths, because the two verbs ask different questions:
//
//   - Detail — everything known, for `describe`.
//   - Review — the verdict and the report, for `review`.
//
// They share this package's helpers rather than each verb growing its own copy,
// which is what previously let two spellings of "render an analysis rating"
// drift apart.
package render
