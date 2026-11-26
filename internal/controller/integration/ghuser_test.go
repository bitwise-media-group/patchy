// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/ghclient"
	"github.com/bitwise-media-group/patchy/internal/ghuser"
	"github.com/bitwise-media-group/patchy/internal/kube"
)

// fakeGitHubUser is a GitHub with the OAuth endpoints and the user-token
// API, behind the /api/v3 prefix a GHES base URL gets.
type fakeGitHubUser struct {
	srv *httptest.Server
	// roles is repo → permissions JSON for the user.
	roles map[string]string
	// refreshed tracks spent refresh tokens: GitHub accepts each once.
	refreshed map[string]bool
	// alerts answers the App's code-scanning probe.
	alerts int
}

func newFakeGitHubUser(t *testing.T) *fakeGitHubUser {
	t.Helper()
	f := &fakeGitHubUser{roles: map[string]string{}, refreshed: map[string]bool{}, alerts: http.StatusOK}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		if r.Form.Get("client_id") != "Iv1.test" || r.Form.Get("client_secret") != "oauth-secret" {
			_, _ = fmt.Fprint(w, `{"error":"incorrect_client_credentials"}`)
			return
		}
		switch r.Form.Get("grant_type") {
		case "refresh_token":
			rt := r.Form.Get("refresh_token")
			if rt != "ghr_1" || f.refreshed[rt] {
				_, _ = fmt.Fprint(w, `{"error":"bad_refresh_token",`+
					`"error_description":"The refresh token passed is incorrect or expired."}`)
				return
			}
			f.refreshed[rt] = true
			_, _ = fmt.Fprint(w, `{"access_token":"ghu_2","refresh_token":"ghr_2","expires_in":28800,"token_type":"bearer"}`)
		default:
			if r.Form.Get("code") != "good-code" || r.Form.Get("code_verifier") != "verifier" ||
				r.Form.Get("redirect_uri") != "https://status.test/oauth2/github/gh/callback" {
				_, _ = fmt.Fprint(w, `{"error":"bad_verification_code",`+
					`"error_description":"The code passed is incorrect or expired."}`)
				return
			}
			_, _ = fmt.Fprint(w, `{"access_token":"ghu_1","refresh_token":"ghr_1","expires_in":28800,"token_type":"bearer"}`)
		}
	})
	api := http.NewServeMux()
	userOnly := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if tok := r.Header.Get("Authorization"); tok != "Bearer ghu_1" && tok != "Bearer ghu_2" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = fmt.Fprint(w, `{"message":"Bad credentials"}`)
				return
			}
			h(w, r)
		}
	}
	api.HandleFunc("GET /user", userOnly(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"login":"octocat","id":583231}`)
	}))
	api.HandleFunc("GET /user/installations", userOnly(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"total_count":1,"installations":[{"id":7}]}`)
	}))
	api.HandleFunc("GET /user/installations/7/repositories", userOnly(func(w http.ResponseWriter, r *http.Request) {
		// Two pages: the walk must follow the Link header.
		if r.URL.Query().Get("page") == "2" {
			_, _ = fmt.Fprint(w, `{"total_count":3,"repositories":[`+
				`{"full_name":"acme/billing","permissions":{"admin":false,"maintain":true,"push":true,"pull":true}}]}`)
			return
		}
		next := *r.URL
		q := next.Query()
		q.Set("page", "2")
		next.RawQuery = q.Encode()
		w.Header().Set("Link", fmt.Sprintf(`<%s%s>; rel="next"`, f.srv.URL, next.RequestURI()))
		_, _ = fmt.Fprint(w, `{"total_count":3,"repositories":[`+
			`{"full_name":"acme/shop","permissions":{"admin":true,"maintain":true,"push":true,"pull":true}},`+
			`{"full_name":"acme/docs","permissions":{"admin":false,"maintain":false,"push":true,"pull":true}}]}`)
	}))
	api.HandleFunc("GET /repos/{owner}/{name}", userOnly(func(w http.ResponseWriter, r *http.Request) {
		perms, ok := f.roles[r.PathValue("owner")+"/"+r.PathValue("name")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"message":"Not Found"}`)
			return
		}
		_, _ = fmt.Fprintf(w, `{"full_name":%q,"permissions":%s}`, r.PathValue("owner")+"/"+r.PathValue("name"), perms)
	}))
	root := http.NewServeMux()
	root.Handle("/login/", mux)
	root.Handle("/api/v3/", http.StripPrefix("/api/v3", api))
	f.srv = httptest.NewServer(root)
	t.Cleanup(f.srv.Close)
	return f
}

// userFixture is a GitHubUser over a fake client holding a GHES App
// Integration with user authorization, its Secret, and a few refusals.
func userFixture(t *testing.T, gh *fakeGitHubUser) *GitHubUser {
	t.Helper()
	base := gh.srv.URL
	integ := func(name string, mutate func(*v1alpha1.Integration)) *v1alpha1.Integration {
		i := &v1alpha1.Integration{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "patchy", Generation: 1},
			Spec: v1alpha1.IntegrationSpec{
				Provider:  v1alpha1.IntegrationProviderGitHub,
				SecretRef: &v1alpha1.LocalSecretReference{Name: "app"},
				GitHub: &v1alpha1.GitHubIntegration{
					BaseURL:            base,
					CodeScanningAlerts: &v1alpha1.GitHubCodeScanningAlerts{Enabled: true},
					UserAuthorization:  &v1alpha1.GitHubUserAuthorization{ClientID: "Iv1.test"},
				},
			},
		}
		if mutate != nil {
			mutate(i)
		}
		return i
	}
	objs := []client.Object{
		integ("gh", nil),
		integ("gh-suspended", func(i *v1alpha1.Integration) { i.Spec.Suspend = true }),
		integ("gh-plain", func(i *v1alpha1.Integration) { i.Spec.GitHub.UserAuthorization = nil }),
		integ("gh-pat", func(i *v1alpha1.Integration) { i.Spec.SecretRef.Name = "pat" }),
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "patchy"},
			Data: map[string][]byte{
				"appID": []byte("1"), "privateKey": accessPrivateKey(t), "clientSecret": []byte("oauth-secret"),
			},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "pat", Namespace: "patchy"},
			Data:       map[string][]byte{"token": []byte("pat")},
		},
	}
	c := fake.NewClientBuilder().WithScheme(kube.Scheme()).WithObjects(objs...).Build()
	creds := NewCreds(c)
	access := &RepositoryAccess{
		Reader: c, Creds: creds, Namespace: "patchy",
		clientFor: func(context.Context, *v1alpha1.Integration, ghclient.Repo) (alertClient, error) {
			return probeStatus(gh.alerts), nil
		},
	}
	return &GitHubUser{Reader: c, Creds: creds, Access: access, Auth: fakeCallerAuth{}, Namespace: "patchy"}
}

// probeStatus answers every alert probe with one HTTP status.
type probeStatus int

func (p probeStatus) ProbeAlerts(context.Context, ghclient.Repo, ghclient.AlertKind) (ghclient.AccessStatus, string) {
	if p == http.StatusOK {
		return ghclient.AccessOK, ""
	}
	return ghclient.AccessForbidden, "Resource not accessible by integration"
}

// post calls one endpoint through the handler and decodes the answer.
func post(t *testing.T, h http.Handler, path, token string, body any, out any) (int, ghuser.Error) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	if token != "" {
		req.Header.Set(ghuser.TokenHeader, token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var e ghuser.Error
	if rec.Code != http.StatusOK {
		_ = json.Unmarshal(rec.Body.Bytes(), &e)
		return rec.Code, e
	}
	if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
		t.Fatalf("decode %s: %v (%s)", path, err, rec.Body)
	}
	return rec.Code, e
}

func userHandler(g *GitHubUser) http.Handler {
	mux := http.NewServeMux()
	g.Register(mux)
	return mux
}

func TestGitHubUserExchangeAndRefresh(t *testing.T) {
	gh := newFakeGitHubUser(t)
	h := userHandler(userFixture(t, gh))

	var ex ghuser.ExchangeResponse
	code, e := post(t, h, ghuser.PathExchange, "status-server", ghuser.ExchangeRequest{
		Integration: "gh", Code: "good-code", Verifier: "verifier",
		RedirectURI: "https://status.test/oauth2/github/gh/callback",
	}, &ex)
	if code != http.StatusOK {
		t.Fatalf("exchange = %d %+v", code, e)
	}
	if ex.Login != "octocat" || ex.UserID != 583231 || ex.AccessToken != "ghu_1" || ex.RefreshToken != "ghr_1" ||
		ex.ExpiresAt.IsZero() {
		t.Errorf("exchange = %+v", ex)
	}

	// A bad code is the user's grant failing, not an upstream error.
	code, e = post(t, h, ghuser.PathExchange, "status-server", ghuser.ExchangeRequest{
		Integration: "gh", Code: "stale", Verifier: "verifier", RedirectURI: "https://status.test/oauth2/github/gh/callback",
	}, &ex)
	if code != http.StatusForbidden || e.Code != ghuser.CodeUserToken ||
		!strings.Contains(e.Message, "bad_verification_code") {
		t.Errorf("bad code = %d %+v, want 403 user_token", code, e)
	}

	// Refresh rotates the pair; the spent refresh token is refused.
	var tok ghuser.Tokens
	if code, e := post(t, h, ghuser.PathRefresh, "status-server",
		ghuser.RefreshRequest{Integration: "gh", RefreshToken: "ghr_1"}, &tok); code != http.StatusOK {
		t.Fatalf("refresh = %d %+v", code, e)
	}
	if tok.AccessToken != "ghu_2" || tok.RefreshToken != "ghr_2" {
		t.Errorf("refresh = %+v, want the rotated pair", tok)
	}
	code, e = post(t, h, ghuser.PathRefresh, "status-server",
		ghuser.RefreshRequest{Integration: "gh", RefreshToken: "ghr_1"}, &tok)
	if code != http.StatusForbidden || e.Code != ghuser.CodeUserToken {
		t.Errorf("reused refresh = %d %+v, want 403 user_token", code, e)
	}
}

func TestGitHubUserRepositories(t *testing.T) {
	gh := newFakeGitHubUser(t)
	h := userHandler(userFixture(t, gh))

	var res ghuser.RepositoriesResponse
	code, e := post(t, h, ghuser.PathRepositories, "status-server",
		ghuser.RepositoriesRequest{Integration: "gh", AccessToken: "ghu_1"}, &res)
	if code != http.StatusOK {
		t.Fatalf("repositories = %d %+v", code, e)
	}
	// Both pages walked; the write-only repository filtered out.
	want := []ghuser.Repository{{Name: "acme/shop", Role: "admin"}, {Name: "acme/billing", Role: "maintain"}}
	if fmt.Sprint(res.Repositories) != fmt.Sprint(want) || res.Truncated {
		t.Errorf("repositories = %+v, want %+v", res, want)
	}

	// An expired user token is user_token, so the caller can refresh.
	code, e = post(t, h, ghuser.PathRepositories, "status-server",
		ghuser.RepositoriesRequest{Integration: "gh", AccessToken: "ghu_expired"}, &res)
	if code != http.StatusForbidden || e.Code != ghuser.CodeUserToken {
		t.Errorf("expired token = %d %+v, want 403 user_token", code, e)
	}
}

func TestGitHubUserAuthorizeRepository(t *testing.T) {
	gh := newFakeGitHubUser(t)
	gh.roles["acme/shop"] = `{"admin":false,"maintain":true,"push":true,"pull":true}`
	gh.roles["acme/docs"] = `{"admin":false,"maintain":false,"push":true,"pull":true}`
	g := userFixture(t, gh)
	h := userHandler(g)

	cases := []struct {
		name     string
		repo     string
		alerts   int
		role     string
		readable bool
	}{
		{"maintainer, readable", "acme/shop", http.StatusOK, "maintain", true},
		{"maintainer, alerts unreadable", "acme/shop", http.StatusForbidden, "maintain", false},
		{"writer is not probed", "acme/docs", http.StatusOK, "write", false},
		{"invisible repository", "acme/secret", http.StatusOK, "none", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gh.alerts = tc.alerts
			g.Access.cache = nil
			g.Access.once = sync.Once{}
			var res ghuser.AuthorizeRepositoryResponse
			code, e := post(t, h, ghuser.PathAuthorizeRepository, "status-server",
				ghuser.AuthorizeRepositoryRequest{Integration: "gh", AccessToken: "ghu_1", Repo: tc.repo}, &res)
			if code != http.StatusOK {
				t.Fatalf("authorize = %d %+v", code, e)
			}
			if res.Role != tc.role || res.Access.Readable() != tc.readable || res.Access.Name != "gh" {
				t.Errorf("authorize = %+v, want role %s readable %v", res, tc.role, tc.readable)
			}
		})
	}
}

func TestGitHubUserRefusals(t *testing.T) {
	gh := newFakeGitHubUser(t)
	h := userHandler(userFixture(t, gh))
	cases := []struct {
		name, token string
		body        any
		path        string
		wantStatus  int
		wantCode    string
	}{
		{"caller token rejected", "someone-else", ghuser.RepositoriesRequest{Integration: "gh", AccessToken: "ghu_1"},
			ghuser.PathRepositories, http.StatusUnauthorized, ghuser.CodeCaller},
		{"no caller token", "", ghuser.RepositoriesRequest{Integration: "gh", AccessToken: "ghu_1"},
			ghuser.PathRepositories, http.StatusUnauthorized, ghuser.CodeCaller},
		{"suspended integration", "status-server",
			ghuser.RepositoriesRequest{Integration: "gh-suspended", AccessToken: "ghu_1"},
			ghuser.PathRepositories, http.StatusConflict, ghuser.CodeIntegration},
		{"not opted in", "status-server", ghuser.RepositoriesRequest{Integration: "gh-plain", AccessToken: "ghu_1"},
			ghuser.PathRepositories, http.StatusConflict, ghuser.CodeIntegration},
		{"unknown integration", "status-server", ghuser.RepositoriesRequest{Integration: "nope", AccessToken: "ghu_1"},
			ghuser.PathRepositories, http.StatusConflict, ghuser.CodeIntegration},
		{"PAT credential cannot exchange", "status-server", ghuser.ExchangeRequest{Integration: "gh-pat",
			Code: "good-code", Verifier: "verifier", RedirectURI: "https://status.test/oauth2/github/gh/callback"},
			ghuser.PathExchange, http.StatusConflict, ghuser.CodeIntegration},
		{"missing field", "status-server", ghuser.RepositoriesRequest{Integration: "gh"},
			ghuser.PathRepositories, http.StatusBadRequest, ghuser.CodeBadRequest},
		{"prefix repository", "status-server",
			ghuser.AuthorizeRepositoryRequest{Integration: "gh", AccessToken: "x", Repo: "acme/"},
			ghuser.PathAuthorizeRepository, http.StatusBadRequest, ghuser.CodeBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out map[string]any
			code, e := post(t, h, tc.path, tc.token, tc.body, &out)
			if code != tc.wantStatus || e.Code != tc.wantCode {
				t.Errorf("%s = %d %+v, want %d %s", tc.path, code, e, tc.wantStatus, tc.wantCode)
			}
		})
	}
}

func TestCredsUserOAuth(t *testing.T) {
	gh := newFakeGitHubUser(t)
	g := userFixture(t, gh)
	var integ v1alpha1.Integration
	if err := g.Reader.Get(t.Context(), client.ObjectKey{Namespace: "patchy", Name: "gh"}, &integ); err != nil {
		t.Fatal(err)
	}
	integ.Spec.GitHub.Proxy = &v1alpha1.ProxyConfig{URL: "http://proxy.test:3128"}
	o, err := g.Creds.UserOAuth(t.Context(), &integ)
	if err != nil {
		t.Fatal(err)
	}
	// GHES: the OAuth endpoints live on the web root, not under /api/v3.
	u, _ := url.Parse(gh.srv.URL)
	if o.WebURL != "http://"+u.Host || o.ClientID != "Iv1.test" || o.ClientSecret != "oauth-secret" ||
		o.ProxyURL != "http://proxy.test:3128" {
		t.Errorf("UserOAuth = %+v", o)
	}
}

func TestUserAuthorizationCondition(t *testing.T) {
	gh := newFakeGitHubUser(t)
	g := userFixture(t, gh)
	r := &IntegrationReconciler{Creds: g.Creds}
	get := func(name string) *v1alpha1.Integration {
		var integ v1alpha1.Integration
		if err := g.Reader.Get(t.Context(), client.ObjectKey{Namespace: "patchy", Name: name}, &integ); err != nil {
			t.Fatal(err)
		}
		return &integ
	}
	cond := func(i *v1alpha1.Integration) *metav1.Condition {
		for k := range i.Status.Conditions {
			if i.Status.Conditions[k].Type == v1alpha1.ConditionUserAuthorizationReady {
				return &i.Status.Conditions[k]
			}
		}
		return nil
	}

	// GET /app is not served by the fake: the client ID goes unverified,
	// which is not a failure.
	ok := get("gh")
	r.userAuthorizationCondition(t.Context(), ok, true)
	if c := cond(ok); c == nil || c.Status != metav1.ConditionTrue || c.Reason != "ClientIDUnverified" {
		t.Errorf("app integration condition = %+v, want True/ClientIDUnverified", c)
	}
	pat := get("gh-pat")
	r.userAuthorizationCondition(t.Context(), pat, true)
	if c := cond(pat); c == nil || c.Status != metav1.ConditionFalse ||
		!strings.Contains(c.Message, "personal access token") {
		t.Errorf("PAT condition = %+v, want False naming the PAT", c)
	}
	bad := get("gh")
	r.userAuthorizationCondition(t.Context(), bad, false)
	if c := cond(bad); c == nil || c.Status != metav1.ConditionFalse || c.Reason != "CredentialInvalid" {
		t.Errorf("invalid credential condition = %+v", c)
	}
	plain := get("gh-plain")
	plain.Status.Conditions = []metav1.Condition{{Type: v1alpha1.ConditionUserAuthorizationReady,
		Status: metav1.ConditionTrue, Reason: "Configured", LastTransitionTime: metav1.NewTime(time.Now())}}
	r.userAuthorizationCondition(t.Context(), plain, true)
	if c := cond(plain); c != nil {
		t.Errorf("opted-out integration kept the condition: %+v", c)
	}
}
