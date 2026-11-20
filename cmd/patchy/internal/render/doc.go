// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package render turns one patchy resource into a printer document.
//
// It is organised by noun (one file per kind) because a kind is what changes
// together: add a field to InvestigationStatus and exactly one file here needs
// editing. The verb commands stay noun-agnostic, so `patchy get` can serve
// every kind through the resource registry without a per-kind branch.
//
// Each kind offers two depths, because the two verbs ask different questions:
//
//   - Detail: everything known, for `describe`.
//   - Review: the verdict and the report, for `review`.
//
// Both depths share this package's helpers rather than each verb keeping its
// own copy, so the two cannot drift apart.
package render
