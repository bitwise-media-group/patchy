// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package spec defines the mirror store's on-disk contract: the per-entry
// manifest.yaml files (intent), the lock files (facts), the generated
// images.extra.yaml sidecar, and the CVE allowlists — plus the typed blocks
// the layered patchy config shares between mirror and build (registries,
// signing, and the one scan block every entry overrides field by field).
// The tree itself is the registry of entries — discovery globs
// charts/*/manifest.yaml and artifacts/*/manifest.yaml; the config never
// lists them, and this package never discovers or reads the config: the
// CLI assembles a Config and hands it in.
//
// Manifests decode strictly (unknown fields are errors), so a typo'd key
// fails loudly instead of silently reverting to a default. Lock files are
// written through a fixed-shape emitter rather than a generic YAML encoder,
// keeping the bytes stable across regenerations — the validate gate
// byte-compares them.
package spec
