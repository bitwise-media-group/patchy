// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package discover finds every container image a rendered chart references,
// in four passes: pod-spec container lists (containers, initContainers,
// ephemeralContainers — as sequences at any depth, which skips the CRD
// schemas where those keys hold maps), operator-style top-level spec.image
// scalars, a conservative regex sweep over ConfigMap data for image-shaped
// strings (embedded pod templates), and the manifest-declared extras
// ({appVersion} expanded from the vendored chart). References are
// normalized the way container runtimes expand shorthand, filtered by the
// manifest's exclude globs, and returned sorted and deduplicated.
//
// Discovery is pure — rendered bytes in, references out. Digest resolution
// against registries belongs to the engine.
package discover
