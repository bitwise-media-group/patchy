// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package scan finds vulnerabilities in images through pluggable scanners —
// osv-scanner and grype, both shell-outs — either by registry reference
// (ImageScanner: the mirror's locked images) or over a local docker
// archive (ArchiveScanner: a fresh build, scanned before anything is
// pushed). Kubescape optionally sweeps rendered manifests or a manifests
// tree for configuration findings, warn-only by default.
//
// Findings normalize to one shape regardless of scanner: an ID with its
// aliases (CVE/GHSA families cross-reference each other, so an allowlist
// entry written against one scanner's ID keeps working under another),
// a severity normalized to CRITICAL/HIGH/MEDIUM/LOW, and the package,
// installed and fixed-in versions the allowlist derivation needs. The
// scanners never apply policy; Blocking (severities, unfixed handling) and
// Suppress (the allowlist) are the separate steps both engines call.
package scan
