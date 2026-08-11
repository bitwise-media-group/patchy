// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package sign signs published OCI artifacts: keylessly (ambient OIDC —
// the CI workflow's identity — through Fulcio, with a public transparency
// log entry) or with a cloud KMS key (gcpkms://, awskms://, azurekms://).
// Signatures are stored as sigstore bundles attached via the OCI referrers
// API — never legacy .sig tags — the format policy engines' bundle rules
// and modern consumers verify.
//
// The KMS URI schemes register through blank imports of the sigstore KMS
// providers; a signer for tests can point KeyRef at a local cosign
// keypair instead.
package sign
