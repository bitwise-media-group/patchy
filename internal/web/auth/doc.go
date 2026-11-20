// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package auth resolves who is making a status-server request. It owns the
// sign-in surface (the OIDC authorization-code flow with the server as the
// OAuth2 client, the encrypted chunked session cookies, and the small
// SPA-visible state cookies) and hands the rest of the server a bare
// Identity. The package imports nothing from Kubernetes: what an identity
// may do is internal/web/authz's concern.
//
// Four modes exist, selected by the mounted configuration file:
//   - "unconfigured": no file. No session is ever resolved and no sign-in
//     routes are registered, so every data route refuses and only the SPA
//     shell is served.
//   - "none": a fixed identity with authorization bypassed, for development.
//   - "anonymous": a fixed identity that still passes through RBAC access
//     reviews.
//   - "oidc": the real SSO flow.
package auth
