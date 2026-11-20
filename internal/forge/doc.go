// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package forge resolves a finding's repository URL to the Forge custom
// resource whose credentials may act on it, and mints per-operation scoped
// tokens from that Forge's secret.
//
// It is the one shared path to forge access: source-controller resolves read
// scope (clone/tarball), remediation-controller resolves write scope (branch
// push, pull request). Matching is host equality, then the org allowlist, then
// the repository regexes. When several Forges match, the more constrained spec
// wins (orgs set beats unset, then repositories set beats unset). A remaining
// tie is ErrAmbiguous: an operator configuration error, never silently
// resolved.
package forge
