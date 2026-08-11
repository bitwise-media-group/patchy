// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package imageref manipulates container image references as strings: it
// expands docker shorthand the way container runtimes do, splits a reference
// into repository, tag and digest (tolerating registry ports and pinned
// digests), applies source-registry rewrites, and matches shell-style glob
// patterns against references.
//
// Everything here is pure string manipulation — no registry access. The
// mirror engine records canonical (normalized, un-rewritten) references in
// its lock files; rewrites only ever affect where a pull is made from.
package imageref
