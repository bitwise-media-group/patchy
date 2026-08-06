// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"slices"
)

// ErrUnauthenticated is what an Authenticator returns when a delivery fails
// validation. The server answers 401 and says no more: a caller that cannot
// prove who it is gets no diagnostics.
var ErrUnauthenticated = errors.New("delivery failed authentication")

// Authenticator validates one delivery before it is queued. body is the raw
// bytes as received — signature schemes are computed over exactly those, so
// it must not be re-serialized before it gets here.
//
// Returning a non-nil error rejects the delivery with 401; the error is
// logged, never returned to the caller.
type Authenticator interface {
	Authenticate(ctx context.Context, r *http.Request, body []byte) error
}

// AuthenticatorFunc adapts a function to Authenticator.
type AuthenticatorFunc func(ctx context.Context, r *http.Request, body []byte) error

// Authenticate implements Authenticator.
func (f AuthenticatorFunc) Authenticate(ctx context.Context, r *http.Request, body []byte) error {
	return f(ctx, r, body)
}

// Decoder extracts the event type and delivery id from an authenticated
// request. Providers differ on where those live — GitHub uses headers, a
// Pub/Sub push carries the id in the body and needs no type at all — so the
// endpoint supplies the rule rather than the server assuming one.
//
// An empty event type rejects the delivery with 400. An empty delivery id
// skips deduplication, which is the honest answer when a provider does not
// give one.
type Decoder func(r *http.Request, body []byte) (eventType, deliveryID string, err error)

// Endpoint is one provider's receiver route. The whole point of the type is
// that patchy exposes a single internet-facing port: providers differ in how
// they authenticate and label a delivery, not in how it is queued and handled.
type Endpoint struct {
	// Path the provider POSTs to, e.g. "/github/webhooks".
	Path string
	// Auth validates deliveries. Required: an endpoint with no authenticator
	// would accept anything, so NewServer rejects it rather than defaulting
	// to something permissive.
	Auth Authenticator
	// Decode extracts the event type and delivery id. Required.
	Decode Decoder
	// Handler consumes validated deliveries.
	Handler Handler
	// MaxBody caps the request body in bytes; zero means DefaultMaxBody.
	MaxBody int
}

// HMACAuthenticator validates GitHub's X-Hub-Signature-256 against the HMAC of
// the raw body. Secrets are supplied per request because they are
// per-Integration configuration that can change without a restart; the
// candidate set is tiny, and a signature matching any member passes.
type HMACAuthenticator struct {
	// SecretsFor returns the candidate secrets for a delivery.
	SecretsFor func(ctx context.Context) [][]byte
	// SecretsForRequest returns the candidate secrets for a delivery whose
	// candidate set depends on the request — a wildcard route whose path
	// names the integration the secret belongs to. When set it supersedes
	// SecretsFor. Returning an empty set fails authentication, so an
	// unknown path segment is indistinguishable from a bad signature.
	SecretsForRequest func(ctx context.Context, r *http.Request) [][]byte
	// Header carries the signature; empty means X-Hub-Signature-256.
	Header string
}

// Authenticate implements Authenticator.
func (a *HMACAuthenticator) Authenticate(ctx context.Context, r *http.Request, body []byte) error {
	header := a.Header
	if header == "" {
		header = "X-Hub-Signature-256"
	}
	sig := r.Header.Get(header)
	var secrets [][]byte
	switch {
	case a.SecretsForRequest != nil:
		secrets = a.SecretsForRequest(ctx, r)
	case a.SecretsFor != nil:
		secrets = a.SecretsFor(ctx)
	}
	if slices.ContainsFunc(secrets, func(secret []byte) bool {
		return validSignature(secret, body, sig)
	}) {
		return nil
	}
	return ErrUnauthenticated
}

// validSignature checks a "sha256=<hex>" signature against the HMAC of the
// raw body, in constant time.
func validSignature(secret, body []byte, header string) bool {
	if len(secret) == 0 || header == "" {
		return false
	}
	const prefix = "sha256="
	if len(header) <= len(prefix) || header[:len(prefix)] != prefix {
		return false
	}
	want, err := hex.DecodeString(header[len(prefix):])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), want)
}

// GitHubDecoder reads the event type and delivery GUID from GitHub's headers.
func GitHubDecoder(r *http.Request, _ []byte) (eventType, deliveryID string, err error) {
	return r.Header.Get("X-GitHub-Event"), r.Header.Get("X-GitHub-Delivery"), nil
}
