// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package webhook

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
)

// GoogleIssuer is the issuer of the identity tokens Pub/Sub signs.
const GoogleIssuer = "https://accounts.google.com"

// TokenVerifier checks a raw ID token and returns its claims. Narrowed to the
// one method the authenticator needs so tests can sign their own tokens
// without standing up a discovery document and a JWKS.
type TokenVerifier interface {
	Verify(ctx context.Context, rawIDToken string) (*IDTokenClaims, error)
}

// IDTokenClaims are the claims the authenticator decides on. Pub/Sub's push
// tokens are service-account tokens, so the identity is the email.
type IDTokenClaims struct {
	Audience string
	Email    string
	Verified bool `json:"email_verified"`
}

// GoogleOIDCAuthenticator validates the OIDC token Pub/Sub signs for an
// authenticated push subscription.
//
// A push subscription cannot compute an HMAC over the body — the message is
// composed by Pub/Sub, not by the sender — so this is the only authentication
// available on that path, and it is a stronger one: the token is asymmetric,
// short-lived, and bound to both a service account and an audience.
type GoogleOIDCAuthenticator struct {
	// Verify checks the token signature and issuer.
	Verify TokenVerifier
	// Audience the token must carry, matching the subscription's
	// oidcToken.audience.
	Audience string
	// ServiceAccount is the email the token must be issued for. Checking it
	// is what stops any Google-issued token — anyone with a GCP account can
	// mint one — from being accepted.
	ServiceAccount string
}

// Authenticate implements Authenticator.
func (a *GoogleOIDCAuthenticator) Authenticate(ctx context.Context, r *http.Request, _ []byte) error {
	raw, ok := bearerToken(r)
	if !ok {
		return fmt.Errorf("%w: no bearer token", ErrUnauthenticated)
	}
	claims, err := a.Verify.Verify(ctx, raw)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnauthenticated, err)
	}
	// The verifier checks the audience it was built with; re-checking here
	// keeps the authenticator honest against a verifier configured loosely.
	if a.Audience != "" && claims.Audience != a.Audience {
		return fmt.Errorf("%w: token audience %q is not %q",
			ErrUnauthenticated, claims.Audience, a.Audience)
	}
	if a.ServiceAccount != "" {
		if !claims.Verified {
			return fmt.Errorf("%w: token email is unverified", ErrUnauthenticated)
		}
		if !strings.EqualFold(claims.Email, a.ServiceAccount) {
			return fmt.Errorf("%w: token issued for %q, not %q",
				ErrUnauthenticated, claims.Email, a.ServiceAccount)
		}
	}
	return nil
}

// bearerToken extracts the credential from an Authorization header.
func bearerToken(r *http.Request) (string, bool) {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	return strings.TrimSpace(h[len(prefix):]), true
}

// GoogleVerifier is a TokenVerifier backed by Google's OIDC discovery
// document. Verifiers are cached per audience: building one fetches the
// discovery document over the network, which must not happen per delivery.
type GoogleVerifier struct {
	mu        sync.Mutex
	provider  *gooidc.Provider
	verifiers map[string]*gooidc.IDTokenVerifier
	// Issuer overrides the Google issuer; tests point it at a local server.
	Issuer string
	// Audience the verifier is built for.
	Audience string
}

// NewGoogleVerifier builds a verifier for one audience.
func NewGoogleVerifier(audience string) *GoogleVerifier {
	return &GoogleVerifier{Audience: audience, verifiers: map[string]*gooidc.IDTokenVerifier{}}
}

// Verify implements TokenVerifier. The provider is fetched on first use
// rather than at construction, so a network blip at startup cannot stop the
// controller coming up — the delivery is retried by Pub/Sub either way.
func (g *GoogleVerifier) Verify(ctx context.Context, raw string) (*IDTokenClaims, error) {
	v, err := g.verifier(ctx)
	if err != nil {
		return nil, err
	}
	tok, err := v.Verify(ctx, raw)
	if err != nil {
		return nil, err
	}
	var claims struct {
		Email    string `json:"email"`
		Verified bool   `json:"email_verified"`
	}
	if err := tok.Claims(&claims); err != nil {
		return nil, fmt.Errorf("read token claims: %w", err)
	}
	out := &IDTokenClaims{Email: claims.Email, Verified: claims.Verified}
	if len(tok.Audience) > 0 {
		out.Audience = tok.Audience[0]
	}
	return out, nil
}

// verifier returns the cached verifier for the audience, building it once.
func (g *GoogleVerifier) verifier(ctx context.Context) (*gooidc.IDTokenVerifier, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if v, ok := g.verifiers[g.Audience]; ok {
		return v, nil
	}
	if g.provider == nil {
		issuer := g.Issuer
		if issuer == "" {
			issuer = GoogleIssuer
		}
		p, err := gooidc.NewProvider(ctx, issuer)
		if err != nil {
			return nil, fmt.Errorf("discover %s: %w", issuer, err)
		}
		g.provider = p
	}
	v := g.provider.Verifier(&gooidc.Config{ClientID: g.Audience})
	if g.verifiers == nil {
		g.verifiers = map[string]*gooidc.IDTokenVerifier{}
	}
	g.verifiers[g.Audience] = v
	return v, nil
}
