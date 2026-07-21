// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package integration is the integration-controller's engine: the edge
// between patchy and its external systems, driven by Integration resources.
//
// Inbound, the receiver terminates the provider webhook paths
// (/github/webhooks), validates each delivery against the configured
// Integrations' webhook secrets, and turns scanner events into Finding
// resources through the pkg/source handler seam (accumulating alerts into an
// open finding for the accumulation window). Outbound, the Finding
// projection reconciler renders each Finding — and its Investigation and
// Remediation children — as a tracking issue: body, labels, comments,
// assignment, alert dismissal, and closure. Human signals on the tracking
// item (/approve comments, issue close/reopen, pull-request merge) flow back
// as Finding writes.
//
// v1alpha1 simplification: one issues-enabled Integration and one
// code-scanning-enabled Integration per namespace (they are usually the same
// object). Ambiguity surfaces as an error condition rather than a guess; a
// routing field can widen this later.
package integration
