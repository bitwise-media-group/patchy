// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package ghlink

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bitwise-media-group/patchy/internal/ghuser"
	"github.com/bitwise-media-group/patchy/internal/web/auth"
)

var testNow = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

// fakeUsers stands in for the integration-controller.
type fakeUsers struct {
	exchanges atomic.Int32
	refreshes atomic.Int32
	refreshFn func(rt string) (*ghuser.Tokens, error)
	gate      chan struct{} // when set, Refresh waits on it
	lastEx    ghuser.ExchangeRequest
}

func (f *fakeUsers) Exchange(_ context.Context, req ghuser.ExchangeRequest) (*ghuser.ExchangeResponse, error) {
	f.exchanges.Add(1)
	f.lastEx = req
	if req.Code != "good" {
		return nil, &ghuser.Error{Code: ghuser.CodeUserToken, Message: "bad_verification_code"}
	}
	return &ghuser.ExchangeResponse{Login: "octocat", UserID: 1, Tokens: ghuser.Tokens{
		AccessToken: "ghu_1", RefreshToken: "ghr_1", ExpiresAt: testNow.Add(8 * time.Hour),
	}}, nil
}

func (f *fakeUsers) Refresh(_ context.Context, req ghuser.RefreshRequest) (*ghuser.Tokens, error) {
	f.refreshes.Add(1)
	if f.gate != nil {
		<-f.gate
	}
	return f.refreshFn(req.RefreshToken)
}

type harness struct {
	l     *Linker
	users *fakeUsers
	user  string
	now   time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	key, err := auth.SealKey("secret", "test")
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{users: &fakeUsers{}, user: "alice@acme.test", now: testNow}
	h.l = New(Config{
		Key:     key,
		Secure:  true,
		MaxAge:  24 * time.Hour,
		BaseURL: func(*http.Request) string { return "https://status.test" },
		Lookup: func(_ context.Context, name string) (Integration, bool) {
			if name != "gh" {
				return Integration{}, false
			}
			return Integration{Name: "gh", BaseURL: "https://ghes.acme.test", ClientID: "Iv1.abc"}, true
		},
		Identify: func(http.ResponseWriter, *http.Request) (*auth.Identity, error) {
			if h.user == "" {
				return nil, nil
			}
			return &auth.Identity{Username: h.user}, nil
		},
		Users: h.users,
		Now:   func() time.Time { return h.now },
	})
	return h
}

func (h *harness) serve(req *http.Request) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	h.l.Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func cookie(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func errorCookie(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	c := cookie(rec, CookieError)
	if c == nil {
		return ""
	}
	raw, _ := base64.RawURLEncoding.DecodeString(c.Value)
	var msg string
	_ = json.Unmarshal(raw, &msg)
	return msg
}

// authorize runs the first leg and returns the state and CSRF cookie.
func (h *harness) authorize(t *testing.T) (state string, csrf *http.Cookie, target *url.URL) {
	t.Helper()
	rec := h.serve(httptest.NewRequest(http.MethodGet, "/oauth2/github/gh/authorize?originalPath=/%23/repositories", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("authorize = %d %s", rec.Code, rec.Body)
	}
	target, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	csrf = cookie(rec, cookieState)
	if csrf == nil || !csrf.HttpOnly || !csrf.Secure {
		t.Fatalf("state cookie = %+v, want HttpOnly and Secure", csrf)
	}
	return target.Query().Get("state"), csrf, target
}

func (h *harness) callback(state, code string, csrf *http.Cookie, extra string) *httptest.ResponseRecorder {
	q := url.Values{"state": {state}, "code": {code}}
	req := httptest.NewRequest(http.MethodGet, "/oauth2/github/gh/callback?"+q.Encode()+extra, nil)
	if csrf != nil {
		req.AddCookie(csrf)
	}
	return h.serve(req)
}

func TestAuthorizeRedirect(t *testing.T) {
	h := newHarness(t)
	state, _, target := h.authorize(t)
	if target.Host != "ghes.acme.test" || target.Path != "/login/oauth/authorize" {
		t.Errorf("redirect = %s, want the GHES web authorize endpoint", target)
	}
	q := target.Query()
	if q.Get("client_id") != "Iv1.abc" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" ||
		q.Get("redirect_uri") != "https://status.test/oauth2/github/gh/callback" || state == "" {
		t.Errorf("authorize params = %v", q)
	}

	// Signed out, or an unknown Integration: no redirect to GitHub.
	h.user = ""
	rec := h.serve(httptest.NewRequest(http.MethodGet, "/oauth2/github/gh/authorize", nil))
	if rec.Code != http.StatusSeeOther || errorCookie(t, rec) == "" {
		t.Errorf("signed-out authorize = %d, want a redirect home with an error", rec.Code)
	}
	h.user = "alice@acme.test"
	rec = h.serve(httptest.NewRequest(http.MethodGet, "/oauth2/github/other/authorize", nil))
	if rec.Code != http.StatusSeeOther {
		t.Errorf("unknown integration authorize = %d", rec.Code)
	}
}

func TestCallbackLinks(t *testing.T) {
	h := newHarness(t)
	state, csrf, _ := h.authorize(t)
	rec := h.callback(state, "good", csrf, "")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/#/repositories" {
		t.Fatalf("callback = %d → %s (%s)", rec.Code, rec.Header().Get("Location"), errorCookie(t, rec))
	}
	if h.users.lastEx.Verifier == "" || h.users.lastEx.RedirectURI != "https://status.test/oauth2/github/gh/callback" {
		t.Errorf("exchange request = %+v", h.users.lastEx)
	}
	lc := cookie(rec, CookieName("gh"))
	if lc == nil || !lc.HttpOnly || !lc.Secure || lc.MaxAge != int((24*time.Hour).Seconds()) {
		t.Fatalf("link cookie = %+v", lc)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.AddCookie(lc)
	link, ok := h.l.Get(req, auth.Identity{Username: "alice@acme.test"}, "gh")
	if !ok || link.Login != "octocat" || link.AccessToken != "ghu_1" || link.LinkedTo != "alice@acme.test" {
		t.Fatalf("Get = %+v, %v", link, ok)
	}
	// A shared browser: someone else signed in never acts on the link.
	if _, ok := h.l.Get(req, auth.Identity{Username: "bob@acme.test"}, "gh"); ok {
		t.Error("another user read alice's link")
	}
	// Past the session duration the link is gone.
	h.now = testNow.Add(25 * time.Hour)
	if _, ok := h.l.Get(req, auth.Identity{Username: "alice@acme.test"}, "gh"); ok {
		t.Error("link outlived its maximum age")
	}
}

func TestCallbackRefusals(t *testing.T) {
	cases := []struct {
		name string
		run  func(h *harness, state string, csrf *http.Cookie) *httptest.ResponseRecorder
	}{
		{"csrf cookie missing", func(h *harness, state string, _ *http.Cookie) *httptest.ResponseRecorder {
			return h.callback(state, "good", nil, "")
		}},
		{"csrf cookie mismatch", func(h *harness, state string, csrf *http.Cookie) *httptest.ResponseRecorder {
			bad := *csrf
			bad.Value = "forged"
			return h.callback(state, "good", &bad, "")
		}},
		{"tampered state", func(h *harness, state string, csrf *http.Cookie) *httptest.ResponseRecorder {
			return h.callback(state[:len(state)-2]+"xx", "good", csrf, "")
		}},
		{"expired state", func(h *harness, state string, csrf *http.Cookie) *httptest.ResponseRecorder {
			h.now = testNow.Add(stateTTL + time.Minute)
			return h.callback(state, "good", csrf, "")
		}},
		{"provider error", func(h *harness, state string, csrf *http.Cookie) *httptest.ResponseRecorder {
			return h.callback(state, "", csrf, "&error=access_denied&error_description=denied")
		}},
		{"signed in as someone else", func(h *harness, state string, csrf *http.Cookie) *httptest.ResponseRecorder {
			h.user = "bob@acme.test"
			return h.callback(state, "good", csrf, "")
		}},
		{"code rejected", func(h *harness, state string, csrf *http.Cookie) *httptest.ResponseRecorder {
			return h.callback(state, "stale", csrf, "")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			state, csrf, _ := h.authorize(t)
			rec := tc.run(h, state, csrf)
			if rec.Code != http.StatusSeeOther || errorCookie(t, rec) == "" {
				t.Errorf("callback = %d, error %q: want a redirect with an error", rec.Code, errorCookie(t, rec))
			}
			if c := cookie(rec, CookieName("gh")); c != nil && c.MaxAge > 0 {
				t.Error("a refused callback stored a link")
			}
		})
	}
}

func liveLink(h *harness, expiry time.Time) *Link {
	return &Link{Integration: "gh", Login: "octocat", UserID: 1, AccessToken: "ghu_1", RefreshToken: "ghr_1",
		Expiry: expiry, LinkedTo: "alice@acme.test", LinkedAt: h.now}
}

func TestCallRefreshes(t *testing.T) {
	h := newHarness(t)
	h.users.refreshFn = func(rt string) (*ghuser.Tokens, error) {
		return &ghuser.Tokens{AccessToken: "ghu_2", RefreshToken: "ghr_2", ExpiresAt: h.now.Add(8 * time.Hour)}, nil
	}

	// Expiring: refreshed before the call, and the cookie rewritten.
	link := liveLink(h, h.now.Add(30*time.Second))
	rec := httptest.NewRecorder()
	var used []string
	err := h.l.Call(t.Context(), rec, link, func(tok string) error { used = append(used, tok); return nil })
	if err != nil || strings.Join(used, ",") != "ghu_2" || link.RefreshToken != "ghr_2" {
		t.Fatalf("Call = %v, tokens used %v, link %+v", err, used, link)
	}
	if c := cookie(rec, CookieName("gh")); c == nil || c.MaxAge <= 0 {
		t.Error("refreshed link not written back")
	}

	// Rejected mid-life: one refresh, one retry.
	link = liveLink(h, h.now.Add(time.Hour))
	used = nil
	err = h.l.Call(t.Context(), httptest.NewRecorder(), link, func(tok string) error {
		used = append(used, tok)
		if tok == "ghu_1" {
			return &ghuser.Error{Code: ghuser.CodeUserToken}
		}
		return nil
	})
	if err != nil || strings.Join(used, ",") != "ghu_1,ghu_2" {
		t.Errorf("Call = %v, tokens used %v, want a retry after refresh", err, used)
	}
}

func TestCallDropsLinkOnFailedRefresh(t *testing.T) {
	h := newHarness(t)
	h.users.refreshFn = func(string) (*ghuser.Tokens, error) {
		return nil, &ghuser.Error{Code: ghuser.CodeUserToken, Message: "bad_refresh_token"}
	}
	rec := httptest.NewRecorder()
	err := h.l.Call(t.Context(), rec, liveLink(h, h.now.Add(10*time.Second)), func(string) error {
		t.Error("called GitHub with a token that failed to refresh")
		return nil
	})
	if !errors.Is(err, ErrNotLinked) {
		t.Fatalf("Call err = %v, want ErrNotLinked", err)
	}
	if c := cookie(rec, CookieName("gh")); c == nil || c.MaxAge >= 0 {
		t.Errorf("link cookie = %+v, want it cleared", c)
	}
}

// GitHub refresh tokens are single-use: concurrent refreshes of one token
// must share one call, or all but one would drop the link.
func TestConcurrentRefreshShared(t *testing.T) {
	h := newHarness(t)
	h.users.gate = make(chan struct{})
	h.users.refreshFn = func(rt string) (*ghuser.Tokens, error) {
		return &ghuser.Tokens{AccessToken: "ghu_2", RefreshToken: "ghr_2", ExpiresAt: h.now.Add(8 * time.Hour)}, nil
	}
	var wg sync.WaitGroup
	errs := make(chan error, 5)
	for range 5 {
		wg.Go(func() {
			errs <- h.l.Call(t.Context(), httptest.NewRecorder(), liveLink(h, h.now), func(tok string) error {
				if tok != "ghu_2" {
					return errors.New("stale token " + tok)
				}
				return nil
			})
		})
	}
	time.Sleep(20 * time.Millisecond)
	close(h.users.gate)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	if n := h.users.refreshes.Load(); n != 1 {
		t.Errorf("refresh calls = %d, want 1 shared", n)
	}
}

func TestDisconnect(t *testing.T) {
	h := newHarness(t)
	rec := h.serve(httptest.NewRequest(http.MethodPost, "/oauth2/github/gh/disconnect", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("disconnect = %d", rec.Code)
	}
	if c := cookie(rec, CookieName("gh")); c == nil || c.MaxAge >= 0 {
		t.Errorf("link cookie = %+v, want cleared", c)
	}
	rec = h.serve(httptest.NewRequest(http.MethodGet, "/oauth2/github/gh/disconnect", nil))
	if rec.Code == http.StatusNoContent {
		t.Error("disconnect accepted a GET")
	}
}

func TestNewOff(t *testing.T) {
	if New(Config{Users: &fakeUsers{}}) != nil {
		t.Error("a linker without a key is on")
	}
	if New(Config{Key: []byte("k")}) != nil {
		t.Error("a linker without the integration-controller is on")
	}
	var l *Linker
	if _, ok := l.Get(httptest.NewRequest(http.MethodGet, "/", nil), auth.Identity{}, "gh"); ok {
		t.Error("nil linker returned a link")
	}
}

// TestRedirectLocal: the redirect back never leaves the status page, even
// for a path that somehow got past authorize's SafePath.
func TestRedirectLocal(t *testing.T) {
	for in, want := range map[string]string{
		"/#/repositories": "/#/repositories",
		"/a/b?c=d":        "/a/b?c=d",
		"//evil.test/x":   "/",
		"/\\evil.test":    "/",
		"https://evil":    "/",
		"":                "/",
	} {
		w := httptest.NewRecorder()
		redirectLocal(w, httptest.NewRequest(http.MethodGet, "/oauth2/github/gh/callback", nil), in)
		if got := w.Header().Get("Location"); got != want || w.Code != http.StatusSeeOther {
			t.Errorf("redirectLocal(%q) = %d %q, want 303 %q", in, w.Code, got, want)
		}
	}
}
