// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package ocireg is the mirror's one registry seam: thin wrappers over
// go-containerregistry for the operations the engine needs — digest
// resolution, tag listing, config inspection, byte-preserving copies,
// existence probes and raw pulls. Everything takes references as strings
// and a context; authentication rides an injected keychain.
//
// No interface fronts this package: tests run it against an in-memory
// registry (ggcr's registry.New) over httptest, which exercises the real
// wire path instead of a hand-rolled fake.
package ocireg
