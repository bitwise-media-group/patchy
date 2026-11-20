// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package ghas implements the pkg/source plugin for GitHub Advanced Security
// code-scanning alerts (CodeQL first). It consumes code_scanning_alert
// webhook deliveries, fetches the full alert for rule help and location
// detail, and normalizes it into a source.Finding. CWE identifiers from
// CodeQL rule tags become the advisory categorization.
package ghas
