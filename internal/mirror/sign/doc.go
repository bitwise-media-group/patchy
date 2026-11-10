// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package sign signs published OCI artifacts: keylessly (ambient OIDC —
// the CI workflow's identity — through Fulcio, with a public transparency
// log entry) or with a cloud KMS key (gcpkms://, awskms://, azurekms://).
// Signatures are stored as sigstore bundles attached via the OCI referrers
// API — never legacy .sig tags — the format policy engines' bundle rules
// and modern consumers verify.
//
// Signing shells out to the cosign binary (>= v3) with a generated
// sigstore signing config, the only service channel that is not
// deprecated; KMS URI schemes are whatever that cosign build supports. A
// signer for tests can point at a local cosign keypair instead.
//
// Ensurer is the verify-then-sign step mirror sync and build share: a
// reference that already verifies under the resolved signing config is
// left alone, so publishing stays idempotent across re-runs.
package sign
