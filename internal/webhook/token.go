// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package webhook

import (
	"context"
	"crypto/subtle"
	"net/http"
	"slices"
)

// TokenAuthenticator validates the shared bearer token a delivery carries in
// its Authorization header. It exists for providers (Wiz automation actions)
// that send a static header and cannot compute an HMAC over the body. Tokens
// are supplied per request because they are per-Integration configuration
// that can change without a restart; the candidate set is tiny, and a token
// matching any member passes.
type TokenAuthenticator struct {
	// SecretsFor returns the candidate tokens for a delivery.
	SecretsFor func(ctx context.Context) [][]byte
}

// Authenticate implements Authenticator. Only the "Bearer" scheme is
// accepted; each comparison is constant-time.
func (a *TokenAuthenticator) Authenticate(ctx context.Context, r *http.Request, _ []byte) error {
	token, _ := bearerToken(r)
	var secrets [][]byte
	if a.SecretsFor != nil {
		secrets = a.SecretsFor(ctx)
	}
	if slices.ContainsFunc(secrets, func(secret []byte) bool {
		return validToken(secret, token)
	}) {
		return nil
	}
	return ErrUnauthenticated
}

// validToken compares a candidate against the presented token in constant
// time.
func validToken(secret []byte, token string) bool {
	if len(secret) == 0 || token == "" {
		return false
	}
	return subtle.ConstantTimeCompare(secret, []byte(token)) == 1
}
