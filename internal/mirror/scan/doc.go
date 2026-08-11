// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package scan finds vulnerabilities in locked images through pluggable
// scanners: the built-in osv scanner (a library — extraction plus the
// OSV.dev API, no binary needed) and grype (a shell-out, for stores that
// want its database as a second opinion). Kubescape optionally sweeps the
// rendered manifests for configuration findings, warn-only by default.
//
// Findings normalize to one shape regardless of scanner: an ID with its
// aliases (CVE/GHSA families cross-reference each other, so an allowlist
// entry written against one scanner's ID keeps working under another),
// a severity normalized to CRITICAL/HIGH/MEDIUM/LOW, and the package,
// installed and fixed-in versions the allowlist derivation needs. Policy —
// blocking severities, unfixed handling, allowlist suppression — is the
// engine's business, not the scanners'.
package scan
