// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package ocireg is the one registry seam mirror and build share: thin
// wrappers over go-containerregistry for the operations the engines need —
// digest resolution, tag listing, config inspection, byte-preserving
// copies, existence probes, raw pulls, and pushes of locally built images
// and indexes. Everything takes references as strings
// and a context; authentication rides an injected keychain.
//
// No interface fronts this package: tests run it against an in-memory
// registry (ggcr's registry.New) over httptest, which exercises the real
// wire path instead of a hand-rolled fake.
package ocireg
