// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package generic is the wire contract of patchy's generic integration: the
// JSON shapes and headers an external process exchanges with patchy when it
// acts as a finding source, a verdict resolver, or a context enhancer. The
// process can be anything that speaks HTTP — a scheduled job querying a
// warehouse store, an internal CMDB, a vendor bridge — registered as an
// Integration with spec.provider: generic.
//
// Three exchanges, all JSON bodies signed with the integration's shared
// secret (HMAC-SHA256 over the raw body, carried in SignatureHeader):
//
//   - Inbound findings: the process POSTs a Payload of normalized findings
//     to /generic/<integration-name>/webhooks.
//   - Verdict write-back: patchy POSTs a ResolveRequest to the configured
//     resolver URL when a finding is dismissed.
//   - Enhancement: patchy POSTs an EnhanceRequest to the configured enhancer
//     URL for each opened finding; the response body is an EnhanceResponse
//     (204 or an empty body contributes nothing).
//
// The full protocol — validation rules, semantics, and worked signature
// examples — is documented in docs/integrations/generic.md. The exported API
// is deliberately self-contained and depends on nothing under internal/.
package generic
