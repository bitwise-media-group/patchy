// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package mirror is the engine behind `patchy mirror`: vendored mirroring
// of upstream helm charts and OCI artifacts into a platform registry, with
// review-gated updates, upstream provenance verification, vulnerability
// scanning and signing of everything published.
//
// The mirror store is a git tree (the caller's, not this package's
// concern): mirror.yaml holds global defaults, charts/<name>/ and
// artifacts/<name>/ each hold an intent manifest plus machine-derived
// facts (vendored chart tree, rendered manifests, digest locks,
// allowlists). The engine's verbs regenerate facts from intent (upgrade),
// converge the registry onto the committed facts (sync), and re-derive
// out-of-tree to prove the committed facts are current (validate).
//
// The engine never touches git and never creates commits, branches or
// PRs — it mutates files (upgrade, add) or the registry (sync), and the
// calling pipeline owns version control. Wall-clock-dependent steps
// (tracked-tag cooldown picks, allowlist expiry stamping) run only in
// upgrade, keeping validate's byte-identity gate deterministic.
//
// Subpackages hold one concern each; this package orchestrates them and
// owns the Engine API the CLI drives: typed results on stdout's behalf,
// Event narration on stderr's.
package mirror
