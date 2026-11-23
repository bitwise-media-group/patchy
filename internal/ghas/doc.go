// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package ghas implements the pkg/source plugins for GitHub Advanced
// Security.
//
// The code-scanning source ("ghas", Handler) consumes code_scanning_alert
// webhook deliveries, fetches the full alert for rule help and location
// detail, and normalizes it into a source.Finding. CWE identifiers from
// CodeQL rule tags become the advisory categorization.
//
// The secret-scanning source ("ghas-secrets", Secrets) consumes
// secret_scanning_alert deliveries. Its findings carry the secret category,
// accumulate on "secret:<secret type>", and never carry the secret value:
// the delivery is read only for the alert number, and the alert is
// re-fetched with GitHub's hide_secret option.
package ghas
