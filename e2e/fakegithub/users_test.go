// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package fakegithub

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

const (
	testClientID = "Iv-test"
	testSecret   = "test-secret"
	testVerifier = "a-verifier-long-enough-for-pkce-0123456789"
)

func newTestApp(t *testing.T) *Server {
	t.Helper()
	s := New()
	t.Cleanup(s.Close)
	s.RegisterApp(App{ID: 7, ClientID: testClientID, ClientSecret: testSecret, Account: "acme"})
	return s
}

var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}}

// code runs the authorize step for "octo" and returns the issued code.
func code(t *testing.T, s *Server) string {
	t.Helper()
	q := url.Values{
		"client_id": {testClientID}, "redirect_uri": {"http://patchy.test/cb"}, "state": {"st"},
		"code_challenge": {pkceChallenge(testVerifier)}, "code_challenge_method": {"S256"}, "login": {"octo"},
	}
	resp, err := noRedirect.Get(s.URL + "/login/oauth/authorize?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	loc, err := resp.Location()
	if err != nil || resp.StatusCode != http.StatusFound {
		t.Fatalf("authorize = %d (%v)", resp.StatusCode, err)
	}
	if loc.Host != "patchy.test" || loc.Query().Get("state") != "st" {
		t.Fatalf("authorize redirect = %s", loc)
	}
	return loc.Query().Get("code")
}

type tokenAnswer struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Error        string `json:"error"`
}

func token(t *testing.T, s *Server, form url.Values) (int, tokenAnswer) {
	t.Helper()
	form.Set("client_id", testClientID)
	form.Set("client_secret", testSecret)
	resp, err := http.PostForm(s.URL+"/login/oauth/access_token", form)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out tokenAnswer
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, out
}

func apiGet(t *testing.T, s *Server, path, tok string, v any) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, s.URL+"/api/v3"+path, nil)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if v != nil && resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode
}

func TestOAuthCodeGrant(t *testing.T) {
	s := newTestApp(t)
	redirect := "http://patchy.test/cb"

	// A wrong verifier is refused, and spends the code.
	c := code(t, s)
	if st, a := token(t, s, url.Values{"code": {c}, "redirect_uri": {redirect}, "code_verifier": {"wrong"}}); st !=
		http.StatusBadRequest || a.Error != "bad_verification_code" {
		t.Fatalf("wrong verifier = %d %+v", st, a)
	}
	if st, _ := token(t, s, url.Values{"code": {c}, "redirect_uri": {redirect}, "code_verifier": {testVerifier}}); st !=
		http.StatusBadRequest {
		t.Fatalf("spent code = %d, want 400", st)
	}
	// So is a redirect URI other than the authorize step's.
	c = code(t, s)
	if st, a := token(t, s, url.Values{"code": {c}, "redirect_uri": {"http://evil.test/cb"},
		"code_verifier": {testVerifier}}); st != http.StatusBadRequest || a.Error != "redirect_uri_mismatch" {
		t.Fatalf("redirect mismatch = %d %+v", st, a)
	}

	c = code(t, s)
	st, a := token(t, s, url.Values{"code": {c}, "redirect_uri": {redirect}, "code_verifier": {testVerifier}})
	if st != http.StatusOK || !strings.HasPrefix(a.AccessToken, userTokenPrefix) || a.RefreshToken == "" {
		t.Fatalf("exchange = %d %+v", st, a)
	}
	var u struct {
		Login string `json:"login"`
		ID    int64  `json:"id"`
	}
	if apiGet(t, s, "/user", a.AccessToken, &u) != http.StatusOK || u.Login != "octo" || u.ID != s.UserID("octo") {
		t.Fatalf("GET /user = %+v", u)
	}
}

func TestOAuthRefreshRotation(t *testing.T) {
	s := newTestApp(t)
	c := code(t, s)
	_, first := token(t, s, url.Values{"code": {c}, "redirect_uri": {"http://patchy.test/cb"},
		"code_verifier": {testVerifier}})

	st, second := token(t, s, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {first.RefreshToken}})
	if st != http.StatusOK || second.AccessToken == first.AccessToken || second.RefreshToken == first.RefreshToken {
		t.Fatalf("refresh = %d %+v", st, second)
	}
	if st, a := token(t, s, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {first.RefreshToken}}); st !=
		http.StatusBadRequest || a.Error != "bad_refresh_token" {
		t.Fatalf("reused refresh token = %d %+v, want bad_refresh_token", st, a)
	}
	if apiGet(t, s, "/user", first.AccessToken, nil) != http.StatusUnauthorized {
		t.Error("the rotated-out access token still works")
	}
	if apiGet(t, s, "/user", second.AccessToken, nil) != http.StatusOK {
		t.Error("the rotated access token does not work")
	}
	if s.TokenRefreshes() != 1 {
		t.Errorf("refreshes = %d, want 1", s.TokenRefreshes())
	}
	s.ExpireUserTokens()
	if apiGet(t, s, "/repos/acme/shop", second.AccessToken, nil) != http.StatusUnauthorized {
		t.Error("an expired user token reads a repository")
	}
}

func TestUserRepositories(t *testing.T) {
	s := newTestApp(t)
	s.SetUserRole("octo", "acme/shop", RoleMaintain)
	s.SetUserRole("octo", "acme/web", RoleWrite)
	s.SeedAlert("acme", "api", 1)
	c := code(t, s)
	_, a := token(t, s, url.Values{"code": {c}, "redirect_uri": {"http://patchy.test/cb"},
		"code_verifier": {testVerifier}})

	var repo struct {
		Permissions map[string]bool `json:"permissions"`
	}
	if apiGet(t, s, "/repos/acme/shop", a.AccessToken, &repo) != http.StatusOK ||
		!repo.Permissions["maintain"] || repo.Permissions["admin"] {
		t.Errorf("acme/shop permissions = %+v, want maintain", repo.Permissions)
	}
	if apiGet(t, s, "/repos/acme/api", a.AccessToken, nil) != http.StatusNotFound {
		t.Error("a repository the user cannot see is not a 404")
	}
	var list struct {
		Repositories []struct {
			FullName    string          `json:"full_name"`
			Permissions map[string]bool `json:"permissions"`
		} `json:"repositories"`
	}
	if apiGet(t, s, "/user/installations/1/repositories", a.AccessToken, &list) != http.StatusOK ||
		len(list.Repositories) != 2 || list.Repositories[0].FullName != "acme/shop" ||
		list.Repositories[1].FullName != "acme/web" || !list.Repositories[1].Permissions["push"] {
		t.Errorf("user installation repositories = %+v", list.Repositories)
	}
	// The App endpoints want the App's JWT, not a user token.
	if apiGet(t, s, "/app", a.AccessToken, nil) != http.StatusUnauthorized {
		t.Error("GET /app accepted a user token")
	}
}
