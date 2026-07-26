// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package fakeoidc is a minimal OIDC issuer standing in for Google at the
// network edge, the way fakegithub stands in for GitHub.
//
// The Security Command Center route authenticates the token Pub/Sub signs, so
// exercising it end to end needs an issuer the receiver can actually verify
// against: a discovery document, a JWKS, and a signing key to mint tokens
// with. Everything else about the flow — the audience check, the service
// account check, the expiry — is then the real code path rather than a stub.
package fakeoidc

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// keyID names the single signing key; the JWKS advertises it and every token
// references it.
const keyID = "fakeoidc-1"

// Server is a running fake issuer.
type Server struct {
	*httptest.Server
	key *rsa.PrivateKey
}

// Start brings up the issuer. Its URL is the issuer identifier, so callers
// pass it straight to whatever they are configuring.
func Start() (*Server, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("fakeoidc: generate key: %w", err)
	}
	s := &Server{key: key}

	mux := http.NewServeMux()
	// The httptest server's URL is only known after it starts, and the
	// discovery document has to contain it, so the handler reads it lazily.
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"issuer":                                s.URL,
			"jwks_uri":                              s.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
			"response_types_supported":              []string{"id_token"},
			"subject_types_supported":               []string{"public"},
		})
	})
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key:       key.Public(),
			KeyID:     keyID,
			Algorithm: string(jose.RS256),
			Use:       "sig",
		}}})
	})

	s.Server = httptest.NewServer(mux)
	return s, nil
}

// Token mints a signed identity token for a service account and audience,
// shaped like the one a Pub/Sub push subscription presents.
func (s *Server) Token(email, audience string) (string, error) {
	return s.token(email, audience, true, time.Now().Add(time.Hour))
}

// ExpiredToken mints one that is already past its expiry, for asserting that
// the receiver rejects it.
func (s *Server) ExpiredToken(email, audience string) (string, error) {
	return s.token(email, audience, true, time.Now().Add(-time.Minute))
}

// UnverifiedToken mints one whose email claim is unverified — a claim that
// proves nothing about the caller, so the receiver must not accept it.
func (s *Server) UnverifiedToken(email, audience string) (string, error) {
	return s.token(email, audience, false, time.Now().Add(time.Hour))
}

func (s *Server) token(email, audience string, verified bool, expiry time.Time) (string, error) {
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: s.key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader(jose.HeaderKey("kid"), keyID),
	)
	if err != nil {
		return "", fmt.Errorf("fakeoidc: new signer: %w", err)
	}
	now := time.Now()
	claims := struct {
		jwt.Claims
		Email    string `json:"email"`
		Verified bool   `json:"email_verified"`
	}{
		Claims: jwt.Claims{
			Issuer:   s.URL,
			Subject:  email,
			Audience: jwt.Audience{audience},
			IssuedAt: jwt.NewNumericDate(now),
			Expiry:   jwt.NewNumericDate(expiry),
		},
		Email:    email,
		Verified: verified,
	}
	raw, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		return "", fmt.Errorf("fakeoidc: sign: %w", err)
	}
	return raw, nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
