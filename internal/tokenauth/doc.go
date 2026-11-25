// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package tokenauth authenticates in-cluster callers by their projected
// ServiceAccount token. The caller sends an audience-bound token in a
// request header; the Authenticator reviews it with the API server
// (TokenReview), accepts only the configured ServiceAccount subjects, and
// caches each verdict by token hash for a short TTL. Denials are cached too,
// so a caller retrying a bad token does not turn into API-server QPS.
//
// The egress broker authenticates agent pods with it, and the
// integration-controller's internal endpoint authenticates the status
// server. A token is an identity document, not a capability: whoever accepts
// it still decides what that identity may do.
package tokenauth
