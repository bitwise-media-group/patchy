// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package ghuser is the wire contract of the integration-controller's
// internal GitHub-user endpoints, and the client the status server calls
// them with.
//
// A status-page user may link their own GitHub identity through a github
// Integration's GitHub App (the user-to-server OAuth flow, with PKCE). The
// status server holds no forge Secret and makes no GitHub calls, so every
// call that needs the App's client secret or the user's token goes through
// the integration-controller, which holds the Integration's credential and
// proxy settings already: exchanging the code, refreshing the token pair,
// listing the repositories the user maintains, and checking the user's role
// on one repository together with the Integration's alert access to it.
//
// The endpoints live on the same in-cluster listener as the
// repository-access check and accept the same audience-bound projected
// ServiceAccount token (internal/tokenauth). User tokens travel in request
// and response bodies only. The integration-controller never logs or stores
// them; the status server keeps them in a sealed cookie.
//
// The package is stdlib-only (plus internal/repoaccess), so the status
// server does not link the GitHub client.
package ghuser
