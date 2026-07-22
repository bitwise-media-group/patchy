// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// stateTTL bounds one authorization round trip: the sealed state blob and
// its CSRF cookie expire together.
const stateTTL = 5 * time.Minute

// loginState is the sealed OAuth2 state blob: everything the callback needs
// to finish the flow, so the server keeps no per-login state.
type loginState struct {
	// Verifier is the PKCE code verifier.
	Verifier string `json:"verifier"`
	// CSRF is double-submitted via the state cookie.
	CSRF string `json:"csrf"`
	// Nonce binds the ID token to this authorization request.
	Nonce string `json:"nonce"`
	// OriginalPath is where the browser returns after sign-in.
	OriginalPath string `json:"originalPath"`
	// IssuedAt expires abandoned round trips.
	IssuedAt time.Time `json:"issuedAt"`
}

// oidcAuthenticator runs the authorization-code + PKCE flow against one
// configured provider and resolves sessions from the sealed cookie.
type oidcAuthenticator struct {
	cfg      *Config
	oc       *OIDCConfig
	provider *gooidc.Provider
	verifier *gooidc.IDTokenVerifier
	// key seals the state blob and session cookie (HKDF of the client
	// secret).
	key []byte
	// secret is the OAuth2 client secret for the code exchange.
	secret string
	log    *slog.Logger
	now    func() time.Time
}

// newOIDC discovers the issuer and builds the authenticator. Discovery at
// construction means an unreachable issuer fails startup — the pod restarts
// and retries rather than serving a dead sign-in surface.
func newOIDC(ctx context.Context, cfg *Config, log *slog.Logger) (*oidcAuthenticator, error) {
	oc := cfg.OIDC
	secret, err := oc.clientSecret()
	if err != nil {
		return nil, err
	}
	key, err := sealKey(secret, "patchy-status-session")
	if err != nil {
		return nil, err
	}
	provider, err := gooidc.NewProvider(ctx, oc.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery %s: %w", oc.IssuerURL, err)
	}
	return &oidcAuthenticator{
		cfg:      cfg,
		oc:       oc,
		provider: provider,
		verifier: provider.Verifier(&gooidc.Config{ClientID: oc.ClientID}),
		key:      key,
		secret:   secret,
		log:      log,
		now:      time.Now,
	}, nil
}

// Register mounts the sign-in surface. Logout is POST-only so a cross-site
// GET cannot sign the user out.
func (a *oidcAuthenticator) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /oauth2/authorize", a.handleAuthorize)
	mux.HandleFunc("GET /oauth2/callback", a.handleCallback)
	mux.HandleFunc("POST /logout", a.handleLogout)
}

// secure reports whether cookies carry the Secure attribute; only the
// plain-HTTP local-dev posture (cfg.Insecure) drops it.
func (a *oidcAuthenticator) secure() bool {
	return !a.cfg.Insecure
}

// oauth2Config assembles the flow configuration for one request; the
// redirect URL may depend on the request's forwarded headers.
func (a *oidcAuthenticator) oauth2Config(r *http.Request) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     a.oc.ClientID,
		ClientSecret: a.secret,
		Endpoint:     a.provider.Endpoint(),
		RedirectURL:  a.redirectURL(r),
		Scopes:       a.oc.Scopes,
	}
}

// redirectURL is the configured override or the request-derived callback.
// Deriving trusts X-Forwarded-Proto/-Host, which the deployment docs call
// out as requiring a trusted fronting proxy.
func (a *oidcAuthenticator) redirectURL(r *http.Request) string {
	if a.oc.RedirectURL != "" {
		return a.oc.RedirectURL
	}
	scheme := r.Header.Get("X-Forwarded-Proto")
	if scheme == "" {
		scheme = "http"
		if r.TLS != nil {
			scheme = "https"
		}
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	return scheme + "://" + host + "/oauth2/callback"
}

// handleAuthorize starts one authorization round trip: PKCE verifier, CSRF
// token, and nonce sealed into the state blob, the CSRF half double-submitted
// via a short-lived cookie, then off to the provider.
func (a *oidcAuthenticator) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	csrf, err1 := randomToken()
	nonce, err2 := randomToken()
	if err := errors.Join(err1, err2); err != nil {
		a.fail(w, r, "sign-in could not start", err)
		return
	}
	st := loginState{
		Verifier:     oauth2.GenerateVerifier(),
		CSRF:         csrf,
		Nonce:        nonce,
		OriginalPath: safePath(r.URL.Query().Get("originalPath")),
		IssuedAt:     a.now(),
	}
	blob, err := seal(a.key, st)
	if err != nil {
		a.fail(w, r, "sign-in could not start", err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookieOAuthState,
		Value:    csrf,
		Path:     "/oauth2/",
		MaxAge:   int(stateTTL.Seconds()),
		HttpOnly: true,
		Secure:   a.secure(),
		SameSite: http.SameSiteLaxMode,
	})
	opts := []oauth2.AuthCodeOption{
		oauth2.S256ChallengeOption(st.Verifier),
		gooidc.Nonce(nonce),
	}
	for k, v := range a.oc.AuthURLParams {
		opts = append(opts, oauth2.SetAuthURLParam(k, v))
	}
	http.Redirect(w, r, a.oauth2Config(r).AuthCodeURL(blob, opts...), http.StatusFound)
}

// handleCallback finishes the round trip: state unseal + CSRF double-submit,
// code exchange with the PKCE verifier, ID-token + nonce verification, then
// the session cookie and the guarded redirect home.
func (a *oidcAuthenticator) handleCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	clearCookie(w, cookieOAuthState, a.secure())
	if e := q.Get("error"); e != "" {
		desc := q.Get("error_description")
		a.fail(w, r, fmt.Sprintf("provider rejected sign-in: %s", strings.TrimSpace(e+" "+desc)), nil)
		return
	}
	var st loginState
	if err := unseal(a.key, q.Get("state"), &st); err != nil {
		a.fail(w, r, "sign-in state was invalid", err)
		return
	}
	if a.now().After(st.IssuedAt.Add(stateTTL)) {
		a.fail(w, r, "sign-in took too long, try again", nil)
		return
	}
	csrf, err := r.Cookie(cookieOAuthState)
	if err != nil || csrf.Value == "" || csrf.Value != st.CSRF {
		a.fail(w, r, "sign-in state mismatch, try again", errors.New("csrf double-submit failed"))
		return
	}
	token, err := a.oauth2Config(r).Exchange(r.Context(), q.Get("code"), oauth2.VerifierOption(st.Verifier))
	if err != nil {
		a.fail(w, r, "code exchange failed", err)
		return
	}
	rawID, _ := token.Extra("id_token").(string)
	if rawID == "" {
		a.fail(w, r, "provider returned no ID token", nil)
		return
	}
	idToken, err := a.verifier.Verify(r.Context(), rawID)
	if err != nil {
		a.fail(w, r, "ID token failed verification", err)
		return
	}
	if idToken.Nonce != st.Nonce {
		a.fail(w, r, "ID token nonce mismatch", nil)
		return
	}
	if _, err := a.identityFrom(idToken); err != nil {
		a.fail(w, r, "signed in, but the token is missing required claims", err)
		return
	}
	err = a.writeSession(w, session{
		IDToken:      rawID,
		RefreshToken: token.RefreshToken,
		Start:        a.now(),
	})
	if err != nil {
		a.fail(w, r, "session could not be stored", err)
		return
	}
	a.setProviderCookie(w, r, true)
	clearCookie(w, CookieAuthError, a.secure())
	clearCookie(w, CookieLogout, a.secure())
	http.Redirect(w, r, st.OriginalPath, http.StatusSeeOther)
}

// handleLogout drops the session and pauses autoLogin until the SPA consumes
// the marker.
func (a *oidcAuthenticator) handleLogout(w http.ResponseWriter, r *http.Request) {
	clearChunked(w, a.secure())
	_ = setJSONCookie(w, CookieLogout, true, time.Hour, a.secure())
	a.setProviderCookie(w, r, false)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// Identify resolves the session cookie: verify the ID token, renew it via
// the refresh token when only expiry failed, and re-derive the identity.
// Every failure path clears the session and reports "no session" — a broken
// cookie must land the user on the sign-in panel, not a 500.
func (a *oidcAuthenticator) Identify(w http.ResponseWriter, r *http.Request) (*Identity, error) {
	s, ok := a.readSession(r)
	if !ok {
		a.setProviderCookie(w, r, false)
		return nil, nil
	}
	idToken, err := a.verifier.Verify(r.Context(), s.IDToken)
	var expired *gooidc.TokenExpiredError
	if errors.As(err, &expired) && s.RefreshToken != "" {
		idToken, err = a.refresh(w, r, &s)
	}
	if err != nil {
		a.log.LogAttrs(r.Context(), slog.LevelInfo, "session rejected", slog.Any("error", err))
		clearChunked(w, a.secure())
		a.setProviderCookie(w, r, false)
		return nil, nil
	}
	id, err := a.identityFrom(idToken)
	if err != nil {
		a.log.LogAttrs(r.Context(), slog.LevelWarn, "session claims unusable", slog.Any("error", err))
		clearChunked(w, a.secure())
		a.setProviderCookie(w, r, false)
		return nil, nil
	}
	a.setProviderCookie(w, r, true)
	return id, nil
}

// refresh renews an expired ID token and rewrites the session cookie in
// place, preserving the session start so the absolute lifetime holds.
func (a *oidcAuthenticator) refresh(w http.ResponseWriter, r *http.Request, s *session) (*gooidc.IDToken, error) {
	token, err := a.oauth2Config(r).TokenSource(r.Context(), &oauth2.Token{RefreshToken: s.RefreshToken}).Token()
	if err != nil {
		return nil, fmt.Errorf("refresh token: %w", err)
	}
	rawID, _ := token.Extra("id_token").(string)
	if rawID == "" {
		return nil, errors.New("refresh returned no ID token")
	}
	idToken, err := a.verifier.Verify(r.Context(), rawID)
	if err != nil {
		return nil, fmt.Errorf("refreshed ID token: %w", err)
	}
	s.IDToken = rawID
	if token.RefreshToken != "" {
		s.RefreshToken = token.RefreshToken
	}
	if err := a.writeSession(w, *s); err != nil {
		return nil, err
	}
	return idToken, nil
}

// identityFrom maps the verified token's claims onto an Identity.
func (a *oidcAuthenticator) identityFrom(idToken *gooidc.IDToken) (*Identity, error) {
	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("decode claims: %w", err)
	}
	return mapClaims(claims, a.oc.Claims)
}

// setProviderCookie keeps the SPA's view of the sign-in surface current,
// rewriting only on change to avoid a Set-Cookie on every response.
func (a *oidcAuthenticator) setProviderCookie(w http.ResponseWriter, r *http.Request, authenticated bool) {
	want := providerState{Provider: "oidc", Authenticated: authenticated, AutoLogin: a.oc.AutoLogin}
	var have providerState
	if readJSONCookie(r, CookieProvider, &have) && have == want {
		return
	}
	_ = setJSONCookie(w, CookieProvider, want, 0, a.secure())
}

// fail records a sign-in failure for the SPA and sends the browser home.
func (a *oidcAuthenticator) fail(w http.ResponseWriter, r *http.Request, msg string, err error) {
	a.log.LogAttrs(r.Context(), slog.LevelWarn, "sign-in failed",
		slog.String("reason", msg), slog.Any("error", err))
	_ = setJSONCookie(w, CookieAuthError, msg, time.Minute, a.secure())
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// safePath guards the post-login redirect against open redirects: only
// same-origin absolute paths survive.
func safePath(p string) string {
	if p == "" || !strings.HasPrefix(p, "/") ||
		strings.HasPrefix(p, "//") || strings.HasPrefix(p, "/\\") {
		return "/"
	}
	return p
}
