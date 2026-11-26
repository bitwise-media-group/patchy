// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package ghlink is the status page's "Connect GitHub" link: an optional
// GitHub identity a signed-in user attaches, per github Integration, through
// that Integration's GitHub App user-to-server OAuth flow (PKCE).
//
// Sign-in stays with the configured provider (dex, say), whose groups drive
// the cluster RBAC. Dex issues its own tokens and never hands the upstream
// GitHub token to the client, so acting as the user on GitHub needs this
// separate link. The status server builds the authorize redirect from the
// Integration's spec (the client ID is not secret); the code exchange and
// every token refresh go through the integration-controller's internal
// endpoints (internal/ghuser), which hold the App's client secret.
//
// The link lives in one sealed, HttpOnly cookie per Integration, bound to
// the patchy username it was made for: a link made by another user of a
// shared browser is ignored. Tokens refresh lazily, only on requests that
// call GitHub. GitHub refresh tokens are single-use, so concurrent refreshes
// of one token share a single call, and a refresh GitHub rejects drops the
// link.
package ghlink
