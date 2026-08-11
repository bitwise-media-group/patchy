// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package verify checks OCI artifact signatures for the mirror: upstream
// provenance per a manifest's verifyUpstream rules (keyless with an
// identity regexp, or a published public key), and the mirror's own
// signatures (the exact certificate identity or KMS key it signs with —
// the publish-idempotency self-check).
//
// Verification drives the same code paths the cosign CLI drives: sigstore
// bundles attached via OCI referrers are discovered first, with fallback
// to legacy .sig tags, and a rule may point at a dedicated signature
// repository for publishers that store signatures away from the artifact.
// Keyless verification fetches the public trust roots (TUF); key-based
// verification is offline.
package verify
