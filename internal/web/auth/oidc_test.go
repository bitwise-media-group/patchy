// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

// fakeIssuer is a minimal OIDC provider: discovery, JWKS, and a token
// endpoint minting RS256 ID tokens from test-controlled claims.
type fakeIssuer struct {
	ts  *httptest.Server
	key *rsa.PrivateKey

	mu sync.Mutex
	// claims for the next minted ID token; iss/aud/iat are filled in.
	claims map[string]any
	// exp offset for the next token.
	expIn time.Duration
	// refreshToken returned alongside tokens ("" omits it).
	refreshToken string
	tokenCalls   int
}

func newFakeIssuer(t *testing.T) *fakeIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa key: %v", err)
	}
	fi := &fakeIssuer{key: key, expIn: time.Hour}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		iss := fi.ts.URL
		writeAny(w, map[string]any{
			"issuer":                                iss,
			"authorization_endpoint":                iss + "/auth",
			"token_endpoint":                        iss + "/token",
			"jwks_uri":                              iss + "/keys",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("GET /keys", func(w http.ResponseWriter, _ *http.Request) {
		writeAny(w, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key: key.Public(), KeyID: "test", Algorithm: "RS256", Use: "sig",
		}}})
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, _ *http.Request) {
		fi.mu.Lock()
		defer fi.mu.Unlock()
		fi.tokenCalls++
		resp := map[string]any{
			"access_token": "at",
			"token_type":   "Bearer",
			"expires_in":   3600,
			"id_token":     fi.mintLocked(t, fi.claims, fi.expIn),
		}
		if fi.refreshToken != "" {
			resp["refresh_token"] = fi.refreshToken
		}
		writeAny(w, resp)
	})
	fi.ts = httptest.NewServer(mux)
	t.Cleanup(fi.ts.Close)
	return fi
}

func writeAny(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// mintLocked signs an ID token with iss/aud/iat/exp filled in.
func (fi *fakeIssuer) mintLocked(t *testing.T, claims map[string]any, expIn time.Duration) string {
	t.Helper()
	all := map[string]any{
		"iss": fi.ts.URL,
		"aud": "patchy",
		"iat": time.Now().Add(-time.Minute).Unix(),
		"exp": time.Now().Add(expIn).Unix(),
	}
	maps.Copy(all, claims)
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: fi.key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "test"))
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	payload, err := json.Marshal(all)
	if err != nil {
		t.Fatalf("claims: %v", err)
	}
	jws, err := signer.Sign(payload)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	raw, err := jws.CompactSerialize()
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	return raw
}

// mint signs a token outside the endpoint (for hand-built sessions).
func (fi *fakeIssuer) mint(t *testing.T, claims map[string]any, expIn time.Duration) string {
	fi.mu.Lock()
	defer fi.mu.Unlock()
	return fi.mintLocked(t, claims, expIn)
}

// setNext controls the next token response.
func (fi *fakeIssuer) setNext(claims map[string]any, refreshToken string) {
	fi.mu.Lock()
	defer fi.mu.Unlock()
	fi.claims = claims
	fi.refreshToken = refreshToken
}

// newTestOIDC builds the authenticator + mux against the fake issuer.
func newTestOIDC(t *testing.T, fi *fakeIssuer) (*oidcAuthenticator, *http.ServeMux) {
	t.Helper()
	cfg := &Config{
		Mode:     ModeOIDC,
		Insecure: true,
		OIDC: &OIDCConfig{
			IssuerURL:    fi.ts.URL,
			ClientID:     "patchy",
			ClientSecret: "s3cret",
		},
	}
	cfg.applyDefaults()
	a, err := newOIDC(t.Context(), cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("newOIDC: %v", err)
	}
	mux := http.NewServeMux()
	a.Register(mux)
	return a, mux
}

// authorize drives GET /oauth2/authorize and returns the redirect URL and
// the state cookie.
func authorize(t *testing.T, mux *http.ServeMux, originalPath string) (*url.URL, *http.Cookie) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet,
		"/oauth2/authorize?originalPath="+url.QueryEscape(originalPath), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("authorize status = %d", rec.Code)
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("authorize location: %v", err)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieOAuthState {
			return loc, c
		}
	}
	t.Fatal("authorize set no state cookie")
	return nil, nil
}

// callback drives GET /oauth2/callback with the given state and cookie.
func callback(t *testing.T, mux *http.ServeMux, state string, stateCookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet,
		"/oauth2/callback?code=authcode&state="+url.QueryEscape(state), nil)
	if stateCookie != nil {
		req.AddCookie(&http.Cookie{Name: stateCookie.Name, Value: stateCookie.Value})
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// sessionRequest carries rec's surviving cookies onto a new request.
func sessionRequest(t *testing.T, rec *httptest.ResponseRecorder, target string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for _, c := range rec.Result().Cookies() {
		if c.MaxAge >= 0 && c.Value != "" {
			req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
		}
	}
	return req
}

func userClaims(nonce string) map[string]any {
	return map[string]any{
		"email": "dev@acme.test", "name": "Dev",
		"groups": []string{"patchy-approvers"}, "nonce": nonce,
	}
}

func TestOIDCFullFlow(t *testing.T) {
	fi := newFakeIssuer(t)
	a, mux := newTestOIDC(t, fi)

	loc, stateCookie := authorize(t, mux, "/finding/x?tab=alerts")
	q := loc.Query()
	if loc.Path != "/auth" || q.Get("code_challenge_method") != "S256" || q.Get("nonce") == "" {
		t.Fatalf("authorize redirect = %v", loc)
	}

	fi.setNext(userClaims(q.Get("nonce")), "refresh-1")
	rec := callback(t, mux, q.Get("state"), stateCookie)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/finding/x?tab=alerts" {
		t.Fatalf("callback = %d → %q", rec.Code, rec.Header().Get("Location"))
	}

	id, err := a.Identify(httptest.NewRecorder(), sessionRequest(t, rec, "/api/findings"))
	if err != nil || id == nil {
		t.Fatalf("Identify = %+v, %v", id, err)
	}
	if id.Username != "dev@acme.test" || id.DisplayName != "Dev" ||
		len(id.Groups) != 1 || !id.Session {
		t.Errorf("identity = %+v", id)
	}

	// The provider cookie reports authenticated to the SPA.
	var ps providerState
	if !readJSONCookie(sessionRequest(t, rec, "/"), CookieProvider, &ps) || !ps.Authenticated {
		t.Errorf("provider cookie = %+v", ps)
	}
}

func TestOIDCCallbackRejections(t *testing.T) {
	fi := newFakeIssuer(t)
	a, mux := newTestOIDC(t, fi)

	assertFailed := func(t *testing.T, rec *httptest.ResponseRecorder) {
		t.Helper()
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
			t.Fatalf("failure = %d → %q, want redirect home", rec.Code, rec.Header().Get("Location"))
		}
		found := false
		for _, c := range rec.Result().Cookies() {
			if c.Name == CookieAuthError && c.Value != "" {
				found = true
			}
		}
		if !found {
			t.Error("failure set no auth-error cookie")
		}
		if id, _ := a.Identify(httptest.NewRecorder(), sessionRequest(t, rec, "/")); id != nil {
			t.Errorf("failure produced a session: %+v", id)
		}
	}

	t.Run("csrf mismatch", func(t *testing.T) {
		loc, stateCookie := authorize(t, mux, "/")
		fi.setNext(userClaims(loc.Query().Get("nonce")), "")
		stateCookie.Value = "tampered"
		assertFailed(t, callback(t, mux, loc.Query().Get("state"), stateCookie))
	})
	t.Run("missing state cookie", func(t *testing.T) {
		loc, _ := authorize(t, mux, "/")
		fi.setNext(userClaims(loc.Query().Get("nonce")), "")
		assertFailed(t, callback(t, mux, loc.Query().Get("state"), nil))
	})
	t.Run("nonce mismatch", func(t *testing.T) {
		loc, stateCookie := authorize(t, mux, "/")
		fi.setNext(userClaims("wrong-nonce"), "")
		assertFailed(t, callback(t, mux, loc.Query().Get("state"), stateCookie))
	})
	t.Run("garbage state", func(t *testing.T) {
		_, stateCookie := authorize(t, mux, "/")
		assertFailed(t, callback(t, mux, "garbage", stateCookie))
	})
	t.Run("provider error", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/oauth2/callback?error=access_denied", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assertFailed(t, rec)
	})
	t.Run("token missing username claim", func(t *testing.T) {
		loc, stateCookie := authorize(t, mux, "/")
		fi.setNext(map[string]any{"nonce": loc.Query().Get("nonce")}, "")
		assertFailed(t, callback(t, mux, loc.Query().Get("state"), stateCookie))
	})
}

func TestOIDCOpenRedirectGuard(t *testing.T) {
	fi := newFakeIssuer(t)
	_, mux := newTestOIDC(t, fi)
	for _, path := range []string{"//evil.test/x", "/\\evil.test", "https://evil.test", ""} {
		loc, stateCookie := authorize(t, mux, path)
		fi.setNext(userClaims(loc.Query().Get("nonce")), "")
		rec := callback(t, mux, loc.Query().Get("state"), stateCookie)
		if got := rec.Header().Get("Location"); got != "/" {
			t.Errorf("originalPath %q redirected to %q, want /", path, got)
		}
	}
}

func TestOIDCRefresh(t *testing.T) {
	fi := newFakeIssuer(t)
	a, mux := newTestOIDC(t, fi)
	_ = mux

	// A session whose ID token has expired but which holds a refresh token.
	expired := fi.mint(t, userClaims(""), -time.Hour)
	rec := httptest.NewRecorder()
	err := a.writeSession(rec, session{
		IDToken:      expired,
		RefreshToken: "refresh-1",
		Start:        a.now().Add(-time.Hour),
	})
	if err != nil {
		t.Fatalf("writeSession: %v", err)
	}

	fi.setNext(userClaims(""), "refresh-2")
	out := httptest.NewRecorder()
	id, err := a.Identify(out, sessionRequest(t, rec, "/api/findings"))
	if err != nil || id == nil {
		t.Fatalf("Identify after refresh = %+v, %v", id, err)
	}
	if fi.tokenCalls != 1 {
		t.Errorf("token endpoint calls = %d, want 1 refresh", fi.tokenCalls)
	}
	// The session cookie was rewritten with the fresh token.
	if got := readChunked(sessionRequest(t, out, "/")); got == "" {
		t.Error("refresh did not rewrite the session cookie")
	}
}

func TestOIDCExpiredSessionWithoutRefresh(t *testing.T) {
	fi := newFakeIssuer(t)
	a, _ := newTestOIDC(t, fi)

	expired := fi.mint(t, userClaims(""), -time.Hour)
	rec := httptest.NewRecorder()
	if err := a.writeSession(rec, session{IDToken: expired, Start: a.now()}); err != nil {
		t.Fatalf("writeSession: %v", err)
	}
	out := httptest.NewRecorder()
	id, err := a.Identify(out, sessionRequest(t, rec, "/api/findings"))
	if err != nil || id != nil {
		t.Errorf("Identify = %+v, %v; want no session", id, err)
	}
	if fi.tokenCalls != 0 {
		t.Errorf("token endpoint called %d times without a refresh token", fi.tokenCalls)
	}
}

func TestOIDCSessionAbsoluteLifetime(t *testing.T) {
	fi := newFakeIssuer(t)
	a, _ := newTestOIDC(t, fi)
	a.cfg.SessionDuration.Duration = time.Hour

	valid := fi.mint(t, userClaims(""), time.Hour)
	blob, err := seal(a.key, session{
		IDToken:      valid,
		RefreshToken: "refresh-1",
		Start:        a.now().Add(-2 * time.Hour),
	})
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/findings", nil)
	req.AddCookie(&http.Cookie{Name: cookieSession, Value: blob})
	if id, _ := a.Identify(httptest.NewRecorder(), req); id != nil {
		t.Errorf("expired session identified: %+v", id)
	}
}

func TestOIDCLogout(t *testing.T) {
	fi := newFakeIssuer(t)
	_, mux := newTestOIDC(t, fi)

	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("logout = %d", rec.Code)
	}
	clearedSession, logoutMarker := false, false
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieSession && c.MaxAge < 0 {
			clearedSession = true
		}
		if c.Name == CookieLogout && c.Value != "" {
			logoutMarker = true
		}
	}
	if !clearedSession || !logoutMarker {
		t.Errorf("logout cookies: sessionCleared=%v marker=%v", clearedSession, logoutMarker)
	}

	// GET /logout must not exist (CSRF hardening).
	getReq := httptest.NewRequest(http.MethodGet, "/logout", nil)
	getRec := httptest.NewRecorder()
	mux.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusMethodNotAllowed && getRec.Code != http.StatusNotFound {
		t.Errorf("GET /logout = %d, want rejection", getRec.Code)
	}
}

func TestSafePath(t *testing.T) {
	cases := map[string]string{
		"/finding/x": "/finding/x",
		"/":          "/",
		"":           "/",
		"//evil":     "/",
		"/\\evil":    "/",
		"relative":   "/",
		"https://x":  "/",
	}
	for in, want := range cases {
		if got := safePath(in); got != want {
			t.Errorf("safePath(%q) = %q, want %q", in, got, want)
		}
	}
}
