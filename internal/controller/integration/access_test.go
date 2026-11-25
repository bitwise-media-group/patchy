// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/forge"
	"github.com/bitwise-media-group/patchy/internal/ghclient"
	"github.com/bitwise-media-group/patchy/internal/kube"
	"github.com/bitwise-media-group/patchy/internal/repoaccess"
	"github.com/bitwise-media-group/patchy/internal/tokenauth"
)

// fakeCallerAuth admits the token "status-server" only.
type fakeCallerAuth struct{}

func (fakeCallerAuth) Authenticate(r *http.Request, header string) (tokenauth.Identity, error) {
	if r.Header.Get(header) != "status-server" {
		return tokenauth.Identity{}, errors.New("token rejected: identity is not an accepted service account")
	}
	return tokenauth.Identity{User: "system:serviceaccount:patchy:patchy-status-server"}, nil
}

// fakeForges answers Forge coverage from a table keyed by owner/name.
type fakeForges struct {
	contents map[string]string // repo → contents permission; absent = no Forge
	calls    atomic.Int32
}

func (f *fakeForges) Resolve(_ context.Context, _, repoURL string) (*forge.Resolved, error) {
	f.calls.Add(1)
	_, repo, err := forge.ParseRepoURL(repoURL)
	if err != nil {
		return nil, err
	}
	if _, ok := f.contents[repo.String()]; !ok {
		return nil, forge.ErrNoMatch
	}
	return &forge.Resolved{Forge: &v1alpha1.Forge{ObjectMeta: metav1.ObjectMeta{Name: "github"}}, Repo: repo}, nil
}

func (f *fakeForges) ContentsPermission(_ context.Context, res *forge.Resolved) (string, error) {
	return f.contents[res.Repo.String()], nil
}

func accessPrivateKey(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

// accessFixture is a fake GitHub (behind the /api/v3 prefix a GHES base URL
// gets), a PAT Integration and an App Integration pointed at it, and a
// suspended one that must never be probed.
type accessFixture struct {
	access *RepositoryAccess
	forges *fakeForges
	probes atomic.Int32
	now    time.Time
}

func newAccessFixture(t *testing.T) *accessFixture {
	t.Helper()
	fx := &accessFixture{now: time.Unix(1_800_000_000, 0)}
	mux := http.NewServeMux()
	alerts := func(status int, body string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			fx.probes.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}
	}
	mux.HandleFunc("GET /repos/acme/shop/code-scanning/alerts", alerts(http.StatusOK, `[]`))
	mux.HandleFunc("GET /repos/acme/shop/secret-scanning/alerts", alerts(http.StatusOK, `[]`))
	mux.HandleFunc("GET /repos/acme/locked/code-scanning/alerts",
		alerts(http.StatusForbidden, `{"message":"Resource not accessible by integration"}`))
	mux.HandleFunc("GET /repos/acme/locked/secret-scanning/alerts",
		alerts(http.StatusNotFound, `{"message":"Secret scanning is disabled on this repository."}`))
	mux.HandleFunc("GET /repos/acme/shop/installation", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id": 9}`))
	})
	mux.HandleFunc("GET /repos/acme/locked/installation", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	})
	mux.HandleFunc("POST /app/installations/9/access_tokens", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"inst-tok","expires_at":"2100-01-01T00:00:00Z"}`))
	})
	srv := httptest.NewServer(http.StripPrefix("/api/v3", mux))
	t.Cleanup(srv.Close)

	integ := func(name, secret string, suspend bool) *v1alpha1.Integration {
		return &v1alpha1.Integration{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "patchy", Generation: 1},
			Spec: v1alpha1.IntegrationSpec{
				Provider:  v1alpha1.IntegrationProviderGitHub,
				Suspend:   suspend,
				SecretRef: &v1alpha1.LocalSecretReference{Name: secret},
				GitHub: &v1alpha1.GitHubIntegration{
					BaseURL:            srv.URL,
					CodeScanningAlerts: &v1alpha1.GitHubCodeScanningAlerts{Enabled: true},
				},
			},
		}
	}
	pat := integ("gh-pat", "pat-creds", false)
	pat.Spec.GitHub.SecretScanningAlerts = &v1alpha1.GitHubSecretScanningAlerts{Enabled: true}
	app := integ("gh-app", "app-creds", false)
	suspended := integ("gh-off", "pat-creds", true)
	wiz := &v1alpha1.Integration{
		ObjectMeta: metav1.ObjectMeta{Name: "wiz", Namespace: "patchy"},
		Spec:       v1alpha1.IntegrationSpec{Provider: v1alpha1.IntegrationProviderWiz},
	}
	secrets := []*corev1.Secret{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "pat-creds", Namespace: "patchy"},
			Data:       map[string][]byte{"token": []byte("pat-token")},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "app-creds", Namespace: "patchy"},
			Data: map[string][]byte{
				"appID":      []byte("7"),
				"privateKey": accessPrivateKey(t),
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(kube.Scheme()).
		WithObjects(pat, app, suspended, wiz, secrets[0], secrets[1]).Build()
	fx.forges = &fakeForges{contents: map[string]string{"acme/shop": "write", "acme/locked": "read"}}
	fx.access = &RepositoryAccess{
		Reader:    c,
		Creds:     NewCreds(c),
		Forges:    fx.forges,
		Auth:      fakeCallerAuth{},
		Namespace: "patchy",
		now:       func() time.Time { return fx.now },
	}
	return fx
}

func postAccess(t *testing.T, h http.Handler, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, repoaccess.Path, bytes.NewReader(raw))
	if token != "" {
		req.Header.Set(repoaccess.TokenHeader, token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRepositoryAccessRejects(t *testing.T) {
	fx := newAccessFixture(t)
	h := fx.access.Handler()
	many := make([]string, repoaccess.MaxRepositories+1)
	for i := range many {
		many[i] = "acme/r" + string(rune('a'+i))
	}
	tests := []struct {
		name  string
		token string
		body  any
		want  int
	}{
		{"no token", "", repoaccess.Request{Repositories: []string{"acme/shop"}}, http.StatusUnauthorized},
		{"foreign identity", "agent", repoaccess.Request{Repositories: []string{"acme/shop"}}, http.StatusUnauthorized},
		{"empty list", "status-server", repoaccess.Request{}, http.StatusBadRequest},
		{"prefix entry", "status-server", repoaccess.Request{Repositories: []string{"acme/"}}, http.StatusBadRequest},
		{"too many", "status-server", repoaccess.Request{Repositories: many}, http.StatusBadRequest},
		{"malformed", "status-server", "nope", http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if rec := postAccess(t, h, tt.token, tt.body); rec.Code != tt.want {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tt.want, rec.Body.String())
			}
		})
	}
	if n := fx.probes.Load(); n != 0 {
		t.Fatalf("rejected requests reached GitHub %d times", n)
	}
}

func TestRepositoryAccessCheck(t *testing.T) {
	fx := newAccessFixture(t)
	rec := postAccess(t, fx.access.Handler(), "status-server",
		repoaccess.Request{Repositories: []string{"acme/shop", "acme/locked"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var resp repoaccess.Response
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 2 || resp.Results[0].Repo != "acme/shop" || resp.Results[1].Repo != "acme/locked" {
		t.Fatalf("results out of request order: %+v", resp.Results)
	}

	shop, locked := resp.Results[0], resp.Results[1]
	// Suspended and non-github Integrations are never probed; the rest sort
	// by name.
	if got := integrationNames(shop.Integrations); got != "gh-app,gh-pat" {
		t.Fatalf("integrations = %s, want gh-app,gh-pat", got)
	}
	wantCheck(t, "shop app code", shop.Integrations[0].Alerts.CodeScanning, repoaccess.StatusOK)
	if shop.Integrations[0].Alerts.SecretScanning != nil {
		t.Error("gh-app does not ingest secret scanning, but was probed for it")
	}
	wantCheck(t, "shop pat code", shop.Integrations[1].Alerts.CodeScanning, repoaccess.StatusOK)
	wantCheck(t, "shop pat secrets", shop.Integrations[1].Alerts.SecretScanning, repoaccess.StatusOK)
	if name, ok := shop.Backfillable(); !ok || name != "gh-app" {
		t.Fatalf("Backfillable() = %q, %v; want gh-app", name, ok)
	}
	if shop.Forge == nil || shop.Forge.Status != repoaccess.ForgeOK || shop.Forge.Contents != "write" {
		t.Fatalf("shop forge = %+v", shop.Forge)
	}

	// The App is not installed on acme/locked; the PAT sees it but lacks the
	// permission.
	wantCheck(t, "locked app code", locked.Integrations[0].Alerts.CodeScanning, repoaccess.StatusNotInstalled)
	wantCheck(t, "locked pat code", locked.Integrations[1].Alerts.CodeScanning, repoaccess.StatusForbidden)
	if d := locked.Integrations[1].Alerts.CodeScanning.Detail; d != "Resource not accessible by integration" {
		t.Errorf("forbidden detail = %q, want GitHub's message", d)
	}
	wantCheck(t, "locked pat secrets", locked.Integrations[1].Alerts.SecretScanning, repoaccess.StatusNotFound)
	if _, ok := locked.Backfillable(); ok {
		t.Fatal("acme/locked reported backfillable")
	}
	if locked.Forge == nil || locked.Forge.Status != repoaccess.ForgeNoWrite || locked.Forge.Contents != "read" {
		t.Fatalf("locked forge = %+v", locked.Forge)
	}
}

func TestRepositoryAccessForgeMissing(t *testing.T) {
	fx := newAccessFixture(t)
	fx.forges.contents = map[string]string{}
	results, err := fx.access.Check(context.Background(), []string{"acme/shop"})
	if err != nil {
		t.Fatal(err)
	}
	if f := results[0].Forge; f == nil || f.Status != repoaccess.ForgeMissing {
		t.Fatalf("forge = %+v, want missing", f)
	}
	// A Forge gap does not stop the repository being backfillable.
	if _, ok := results[0].Backfillable(); !ok {
		t.Fatal("forge gap blocked backfill")
	}
}

func TestRepositoryAccessCache(t *testing.T) {
	fx := newAccessFixture(t)
	ctx := context.Background()
	repos := []string{"acme/shop", "acme/locked"}
	if _, err := fx.access.Check(ctx, repos); err != nil {
		t.Fatal(err)
	}
	first, forgeCalls := fx.probes.Load(), fx.forges.calls.Load()
	if first == 0 {
		t.Fatal("no probes reached GitHub")
	}

	// Within the TTL, good and bad results alike come from the cache.
	fx.now = fx.now.Add(AccessTTL - time.Second)
	if _, err := fx.access.Check(ctx, repos); err != nil {
		t.Fatal(err)
	}
	if n := fx.probes.Load(); n != first {
		t.Fatalf("probes = %d after a cached check, want %d", n, first)
	}
	if n := fx.forges.calls.Load(); n != forgeCalls {
		t.Fatalf("forge resolves = %d after a cached check, want %d", n, forgeCalls)
	}

	// Past it, the repositories are probed again.
	fx.now = fx.now.Add(2 * time.Second)
	if _, err := fx.access.Check(ctx, repos); err != nil {
		t.Fatal(err)
	}
	if n := fx.probes.Load(); n <= first {
		t.Fatalf("probes = %d after expiry, want more than %d", n, first)
	}
}

func TestAccessCacheEvicts(t *testing.T) {
	c := newAccessCache(2)
	now := time.Unix(0, 0)
	exp := now.Add(time.Minute)
	c.put("a", 1, exp)
	c.put("b", 2, exp)
	c.get("a", now) // a is now the most recent
	c.put("c", 3, exp)
	if _, ok := c.get("b", now); ok {
		t.Error("least recently used entry survived")
	}
	for _, k := range []string{"a", "c"} {
		if _, ok := c.get(k, now); !ok {
			t.Errorf("entry %s evicted", k)
		}
	}
	if _, ok := c.get("a", exp.Add(time.Second)); ok {
		t.Error("expired entry returned")
	}
}

func TestAccessStatusesMatchGHClient(t *testing.T) {
	pairs := map[ghclient.AccessStatus]repoaccess.Status{
		ghclient.AccessOK:           repoaccess.StatusOK,
		ghclient.AccessNotInstalled: repoaccess.StatusNotInstalled,
		ghclient.AccessForbidden:    repoaccess.StatusForbidden,
		ghclient.AccessNotFound:     repoaccess.StatusNotFound,
		ghclient.AccessNoAnalysis:   repoaccess.StatusNoAnalysis,
		ghclient.AccessError:        repoaccess.StatusError,
	}
	for gh, wire := range pairs {
		if string(gh) != string(wire) {
			t.Errorf("ghclient %q != repoaccess %q", gh, wire)
		}
	}
}

func integrationNames(ias []repoaccess.IntegrationAccess) string {
	out := make([]string, 0, len(ias))
	for _, ia := range ias {
		out = append(out, ia.Name)
	}
	return strings.Join(out, ",")
}

func wantCheck(t *testing.T, what string, c *repoaccess.Check, want repoaccess.Status) {
	t.Helper()
	if c == nil {
		t.Fatalf("%s: no check, want %s", what, want)
	}
	if c.Status != want {
		t.Fatalf("%s: status = %s (%s), want %s", what, c.Status, c.Detail, want)
	}
}
