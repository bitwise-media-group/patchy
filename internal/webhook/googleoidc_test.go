// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package webhook

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

// fakeVerifier stands in for Google's OIDC verification: it checks the
// signature and issuer, which the authenticator trusts, and returns claims,
// which the authenticator decides on. Splitting the two is what lets this
// suite run without a network or a JWKS.
type fakeVerifier struct {
	claims *IDTokenClaims
	err    error
	tokens []string
}

func (f *fakeVerifier) Verify(_ context.Context, raw string) (*IDTokenClaims, error) {
	f.tokens = append(f.tokens, raw)
	return f.claims, f.err
}

// request builds a push request carrying an Authorization header.
func request(header string) *http.Request {
	r, _ := http.NewRequest(http.MethodPost, "http://x/google-cloud/webhooks", nil)
	if header != "" {
		r.Header.Set("Authorization", header)
	}
	return r
}

func TestGoogleOIDCAuthenticator(t *testing.T) {
	const (
		audience = "https://patchy.example/google-cloud/webhooks"
		account  = "scc-push@acme-prod.iam.gserviceaccount.com"
	)
	good := &IDTokenClaims{Audience: audience, Email: account, Verified: true}

	t.Run("accepts a token for the configured audience and account", func(t *testing.T) {
		v := &fakeVerifier{claims: good}
		a := &GoogleOIDCAuthenticator{Verify: v, Audience: audience, ServiceAccount: account}
		if err := a.Authenticate(context.Background(), request("Bearer tok"), nil); err != nil {
			t.Fatalf("Authenticate() = %v, want nil", err)
		}
		if len(v.tokens) != 1 || v.tokens[0] != "tok" {
			t.Errorf("verified tokens = %v, want [tok]", v.tokens)
		}
	})

	t.Run("accepts a lowercase bearer scheme", func(t *testing.T) {
		a := &GoogleOIDCAuthenticator{
			Verify: &fakeVerifier{claims: good}, Audience: audience, ServiceAccount: account,
		}
		if err := a.Authenticate(context.Background(), request("bearer tok"), nil); err != nil {
			t.Errorf("Authenticate() = %v, want nil (the scheme is case-insensitive)", err)
		}
	})

	for _, tt := range []struct {
		name   string
		header string
		v      *fakeVerifier
		why    string
	}{
		{
			name: "no Authorization header", header: "",
			v:   &fakeVerifier{claims: good},
			why: "an unauthenticated push must never reach the handler",
		},
		{
			name: "a non-bearer credential", header: "Basic dXNlcjpwdw==",
			v:   &fakeVerifier{claims: good},
			why: "only bearer tokens are Pub/Sub's scheme",
		},
		{
			name: "a token that fails verification", header: "Bearer tok",
			v:   &fakeVerifier{err: errors.New("expired")},
			why: "signature, issuer and expiry are the verifier's call",
		},
		{
			// Anyone with a Google account can mint a token; the audience is
			// what binds one to this endpoint.
			name: "a token for another audience", header: "Bearer tok",
			v: &fakeVerifier{claims: &IDTokenClaims{
				Audience: "https://elsewhere.example", Email: account, Verified: true,
			}},
			why: "an audience mismatch means the token was minted for someone else",
		},
		{
			// And the service account is what binds it to our subscription.
			name: "a token for another service account", header: "Bearer tok",
			v: &fakeVerifier{claims: &IDTokenClaims{
				Audience: audience, Email: "someone-else@evil.example", Verified: true,
			}},
			why: "any GCP principal could otherwise push findings",
		},
		{
			name: "a token whose email is unverified", header: "Bearer tok",
			v: &fakeVerifier{claims: &IDTokenClaims{
				Audience: audience, Email: account, Verified: false,
			}},
			why: "an unverified email claim proves nothing about the caller",
		},
	} {
		t.Run("rejects "+tt.name, func(t *testing.T) {
			a := &GoogleOIDCAuthenticator{Verify: tt.v, Audience: audience, ServiceAccount: account}
			err := a.Authenticate(context.Background(), request(tt.header), nil)
			if err == nil {
				t.Fatalf("Authenticate() = nil, want an error: %s", tt.why)
			}
			if !errors.Is(err, ErrUnauthenticated) {
				t.Errorf("Authenticate() = %v, want it to wrap ErrUnauthenticated", err)
			}
		})
	}
}

func TestBearerToken(t *testing.T) {
	for _, tt := range []struct {
		header string
		want   string
		ok     bool
	}{
		{"Bearer abc", "abc", true},
		{"bearer abc", "abc", true},
		{"Bearer  abc ", "abc", true},
		{"Basic abc", "", false},
		{"Bearer", "", false},
		{"", "", false},
	} {
		got, ok := bearerToken(request(tt.header))
		if got != tt.want || ok != tt.ok {
			t.Errorf("bearerToken(%q) = (%q, %v), want (%q, %v)", tt.header, got, ok, tt.want, tt.ok)
		}
	}
}
