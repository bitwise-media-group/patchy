// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package evalapi is the evolve-facing HTTP surface of the evaluation
// controller: bearer-authenticated workspace upload (streamed through to
// source-controller's blob cache), submission, snapshot, SSE monitoring, and
// cancellation, speaking the pkg/evaluation wire contract.
//
// Authentication is OIDC bearer verification (go-oidc issuer discovery — no
// provider hard-assumed) mapping claims through the same web/auth
// ClaimsConfig semantics as the status server; authorization is
// SubjectAccessReview on the evaluations resource with native verbs only
// (create/get/delete), so RBAC bindings are the entire authorization
// surface. Mode "none" (dev/e2e) short-circuits both with a fixed identity.
//
// The mux is plain net/http in the internal/web style — these routes are
// synchronous request/response plus SSE, nothing webhook-shaped.
package evalapi
