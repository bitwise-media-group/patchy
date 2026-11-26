// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/ghuser"
	"github.com/bitwise-media-group/patchy/internal/repoaccess"
	"github.com/bitwise-media-group/patchy/internal/web/auth"
	"github.com/bitwise-media-group/patchy/internal/web/authz"
	"github.com/bitwise-media-group/patchy/internal/web/ghlink"
)

// fakeGHUsers is the integration-controller's GitHub-user surface: roles by
// (integration, repo), and a maintained list per integration.
type fakeGHUsers struct {
	mu         sync.Mutex
	roles      map[string]string // integration|repo → role
	unreadable map[string]bool   // integration|repo → alerts unreadable
	maintained map[string][]ghuser.Repository
	reject     map[string]bool // access tokens GitHub rejects
	authCalls  int
	listCalls  int
}

func (f *fakeGHUsers) AuthorizeRepository(
	_ context.Context, req ghuser.AuthorizeRepositoryRequest,
) (*ghuser.AuthorizeRepositoryResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.authCalls++
	if f.reject[req.AccessToken] {
		return nil, &ghuser.Error{Code: ghuser.CodeUserToken}
	}
	key := req.Integration + "|" + req.Repo
	role := f.roles[key]
	if role == "" {
		role = ghuser.RoleNone
	}
	out := &ghuser.AuthorizeRepositoryResponse{Role: role, Access: repoaccess.IntegrationAccess{Name: req.Integration}}
	if ghuser.Maintains(role) && !f.unreadable[key] {
		out.Access.Alerts.CodeScanning = &repoaccess.Check{Status: repoaccess.StatusOK}
	}
	return out, nil
}

func (f *fakeGHUsers) Repositories(
	_ context.Context, req ghuser.RepositoriesRequest,
) (*ghuser.RepositoriesResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls++
	if f.reject[req.AccessToken] {
		return nil, &ghuser.Error{Code: ghuser.CodeUserToken}
	}
	return &ghuser.RepositoriesResponse{Repositories: f.maintained[req.Integration]}, nil
}

func (f *fakeGHUsers) Exchange(context.Context, ghuser.ExchangeRequest) (*ghuser.ExchangeResponse, error) {
	return nil, &ghuser.Error{Code: ghuser.CodeUserToken}
}

func (f *fakeGHUsers) Refresh(context.Context, ghuser.RefreshRequest) (*ghuser.Tokens, error) {
	return nil, &ghuser.Error{Code: ghuser.CodeUserToken, Message: "bad_refresh_token"}
}

// connectableIntegrationFor is a github Integration with user authorization
// ready, on the given base URL ("" = github.com).
func connectableIntegrationFor(name, baseURL string) *v1alpha1.Integration {
	return scanningIntegration(name, func(i *v1alpha1.Integration) {
		i.Spec.GitHub.BaseURL = baseURL
		i.Spec.GitHub.UserAuthorization = &v1alpha1.GitHubUserAuthorization{ClientID: "Iv1." + name}
		i.Status.Conditions = []metav1.Condition{{
			Type: v1alpha1.ConditionUserAuthorizationReady, Status: metav1.ConditionTrue, Reason: "Configured",
			LastTransitionTime: metav1.NewTime(testClock),
		}}
	})
}

// ghServer is a repoServer with GitHub linking wired.
type ghServer struct {
	s     *Server
	users *fakeGHUsers
	key   []byte
}

func newGHServer(t *testing.T, grants authz.Grants, objs ...client.Object) *ghServer {
	t.Helper()
	s := repoServer(t, grants, &fakeAccess{readable: map[string]string{"acme/new": "gh"}}, objs...)
	key, err := auth.SealKey("link", "test")
	if err != nil {
		t.Fatal(err)
	}
	users := &fakeGHUsers{roles: map[string]string{}, unreadable: map[string]bool{},
		maintained: map[string][]ghuser.Repository{}, reject: map[string]bool{}}
	links := ghlink.New(ghlink.Config{
		Key: key, MaxAge: 24 * time.Hour,
		BaseURL:  func(*http.Request) string { return "https://status.test" },
		Lookup:   s.LookupConnectable,
		Identify: s.auth.Identify,
		Users:    users,
		Now:      func() time.Time { return testClock },
	})
	s.WithGitHub(users, links)
	return &ghServer{s: s, users: users, key: key}
}

// link seals a link cookie for the operator on integration.
func (g *ghServer) link(t *testing.T, integration, token string) *http.Cookie {
	t.Helper()
	blob, err := auth.Seal(g.key, ghlink.Link{
		Integration: integration, Login: "octocat", UserID: 7, AccessToken: token, RefreshToken: "ghr",
		Expiry: testClock.Add(8 * time.Hour), LinkedTo: operator.Username, LinkedAt: testClock,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: ghlink.CookieName(integration), Value: blob}
}

func (g *ghServer) do(t *testing.T, method, path, body string, cookies ...*http.Cookie) (int, string) {
	t.Helper()
	ts := httptest.NewServer(g.s.Handler())
	defer ts.Close()
	req, err := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, strings.TrimSpace(string(raw))
}

func maintainerGrants() authz.Grants {
	return authz.Grants{View: true, Maintained: []string{
		"backfill-maintained", "approve-maintained", "expedite-maintained",
	}}
}

func TestMaintainedBackfill(t *testing.T) {
	cases := []struct {
		name      string
		grants    authz.Grants
		linked    []string // integrations the operator linked
		roles     map[string]string
		unread    map[string]bool
		reject    map[string]bool
		want      int
		wantInteg string
		wantRole  string
		wantMsg   string
	}{
		{name: "maintainer with a link", grants: maintainerGrants(), linked: []string{"gh"},
			roles: map[string]string{"gh|acme/new": "maintain"}, want: http.StatusOK, wantInteg: "gh", wantRole: "maintain"},
		{name: "role write", grants: maintainerGrants(), linked: []string{"gh"},
			roles: map[string]string{"gh|acme/new": "write"}, want: http.StatusForbidden, wantMsg: "role is write"},
		{name: "not connected", grants: maintainerGrants(), want: http.StatusForbidden, wantMsg: "Connect GitHub"},
		{name: "alerts unreadable", grants: maintainerGrants(), linked: []string{"gh"},
			roles: map[string]string{"gh|acme/new": "admin"}, unread: map[string]bool{"gh|acme/new": true},
			want: http.StatusForbidden, wantMsg: "cannot read the alerts"},
		{name: "expired link", grants: maintainerGrants(), linked: []string{"gh"},
			reject: map[string]bool{"tok-gh": true}, want: http.StatusForbidden, wantMsg: "reconnect"},
		// Several links: name order, first one that qualifies.
		{name: "first qualifying integration wins", grants: maintainerGrants(), linked: []string{"gh", "gh-b"},
			roles: map[string]string{"gh|acme/new": "write", "gh-b|acme/new": "admin"},
			want:  http.StatusOK, wantInteg: "gh-b", wantRole: "admin"},
		{name: "scoped grant missing", grants: authz.Grants{View: true}, linked: []string{"gh"},
			roles: map[string]string{"gh|acme/new": "admin"}, want: http.StatusForbidden, wantMsg: "Request a backfill"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := newGHServer(t, tc.grants, connectableIntegrationFor("gh", ""), connectableIntegrationFor("gh-b", ""))
			for k, v := range tc.roles {
				g.users.roles[k] = v
			}
			for k, v := range tc.unread {
				g.users.unreadable[k] = v
			}
			for k, v := range tc.reject {
				g.users.reject[k] = v
			}
			var cookies []*http.Cookie
			for _, name := range tc.linked {
				cookies = append(cookies, g.link(t, name, "tok-"+name))
			}
			code, body := g.do(t, http.MethodPost, "/api/repositories/backfill", `{"repository":"acme/new"}`, cookies...)
			if code != tc.want || !strings.Contains(body, tc.wantMsg) {
				t.Fatalf("status = %d (%s), want %d containing %q", code, body, tc.want, tc.wantMsg)
			}
			if tc.want != http.StatusOK {
				return
			}
			var res RepositoryBackfillResult
			_ = json.Unmarshal([]byte(body), &res)
			var bfr v1alpha1.BackfillRequest
			key := types.NamespacedName{Namespace: "patchy", Name: res.Request}
			if err := mustClient(g.s).Get(t.Context(), key, &bfr); err != nil {
				t.Fatal(err)
			}
			if bfr.Spec.IntegrationRef.Name != tc.wantInteg || bfr.Spec.Via != v1alpha1.BackfillViaMaintained ||
				bfr.Spec.GitHub == nil || bfr.Spec.GitHub.Login != "octocat" || bfr.Spec.GitHub.Role != tc.wantRole {
				t.Errorf("request spec = %+v, want maintained on %s as %s", bfr.Spec, tc.wantInteg, tc.wantRole)
			}
		})
	}
}

// A link made by someone else in a shared browser never authorizes.
func TestMaintainedLinkBoundToUser(t *testing.T) {
	g := newGHServer(t, maintainerGrants(), connectableIntegrationFor("gh", ""))
	g.users.roles["gh|acme/new"] = "admin"
	blob, _ := auth.Seal(g.key, ghlink.Link{Integration: "gh", Login: "mallory", AccessToken: "tok",
		LinkedTo: "someone-else@acme.test", LinkedAt: testClock})
	code, body := g.do(t, http.MethodPost, "/api/repositories/backfill", `{"repository":"acme/new"}`,
		&http.Cookie{Name: ghlink.CookieName("gh"), Value: blob})
	if code != http.StatusForbidden || !strings.Contains(body, "Connect GitHub") {
		t.Fatalf("status = %d (%s), want 403: the link belongs to someone else", code, body)
	}
}

func ghFinding(name, repo, integration, host string, phase v1alpha1.Phase) *v1alpha1.Finding {
	return actionFinding(phase, func(f *v1alpha1.Finding) {
		f.Name = name
		f.Spec.IntegrationRef.Name = integration
		if repo != "" {
			f.Spec.Repository = &v1alpha1.FindingRepository{
				Type: v1alpha1.RepositoryTypeGitHub, URL: "https://" + host + "/" + repo, Name: repo,
			}
		}
	})
}

func TestMaintainedFindingActions(t *testing.T) {
	cases := []struct {
		name     string
		grants   authz.Grants
		verb     string
		finding  *v1alpha1.Finding
		linked   []string
		role     string
		want     int
		wantMsg  string
		applied  bool
		roleRepo string
	}{
		{name: "base verb needs no link", grants: allGrants(), verb: "approve",
			finding: ghFinding("f", "acme/shop", "gh", "github.com", v1alpha1.PhaseAwaitingApproval),
			want:    http.StatusOK, applied: true},
		{name: "scoped approve as maintainer", grants: maintainerGrants(), verb: "approve",
			finding: ghFinding("f", "acme/shop", "gh", "github.com", v1alpha1.PhaseAwaitingApproval),
			linked:  []string{"gh"}, role: "maintain", want: http.StatusOK, applied: true},
		{name: "scoped expedite as admin", grants: maintainerGrants(), verb: "expedite",
			finding: ghFinding("f", "acme/shop", "gh", "github.com", v1alpha1.PhaseQueued),
			linked:  []string{"gh"}, role: "admin", want: http.StatusOK, applied: true},
		{name: "scoped with role write", grants: maintainerGrants(), verb: "approve",
			finding: ghFinding("f", "acme/shop", "gh", "github.com", v1alpha1.PhaseAwaitingApproval),
			linked:  []string{"gh"}, role: "write", want: http.StatusForbidden, wantMsg: "role on acme/shop is write"},
		{name: "scoped, not connected", grants: maintainerGrants(), verb: "approve",
			finding: ghFinding("f", "acme/shop", "gh", "github.com", v1alpha1.PhaseAwaitingApproval),
			want:    http.StatusForbidden, wantMsg: "Connect GitHub on github.com"},
		{name: "finding with no repository", grants: maintainerGrants(), verb: "approve",
			finding: ghFinding("f", "", "gh", "", v1alpha1.PhaseAwaitingApproval),
			linked:  []string{"gh"}, role: "admin", want: http.StatusForbidden, wantMsg: "no GitHub repository"},
		// The finding came in through another Integration, but a link on the
		// same host works.
		{name: "same-host link", grants: maintainerGrants(), verb: "approve",
			finding: ghFinding("f", "acme/shop", "gh-unlinked", "github.com", v1alpha1.PhaseAwaitingApproval),
			linked:  []string{"gh"}, role: "maintain", want: http.StatusOK, applied: true},
		{name: "other-host link does not apply", grants: maintainerGrants(), verb: "approve",
			finding: ghFinding("f", "acme/shop", "gh-unlinked", "ghes.acme.test", v1alpha1.PhaseAwaitingApproval),
			linked:  []string{"gh"}, role: "maintain", want: http.StatusForbidden, wantMsg: "Connect GitHub"},
		// The state machine still decides through the scoped path.
		{name: "scoped approve in the wrong phase", grants: maintainerGrants(), verb: "approve",
			finding: ghFinding("f", "acme/shop", "gh", "github.com", v1alpha1.PhaseQueued),
			linked:  []string{"gh"}, role: "maintain", want: http.StatusForbidden, wantMsg: "not available"},
		{name: "retry has no scoped verb", grants: maintainerGrants(), verb: "retry",
			finding: ghFinding("f", "acme/shop", "gh", "github.com", v1alpha1.PhaseFailed),
			linked:  []string{"gh"}, role: "admin", want: http.StatusForbidden, wantMsg: "may not retry"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := newGHServer(t, tc.grants, connectableIntegrationFor("gh", ""), tc.finding)
			g.users.roles["gh|acme/shop"] = tc.role
			var cookies []*http.Cookie
			for _, name := range tc.linked {
				cookies = append(cookies, g.link(t, name, "tok"))
			}
			code, body := g.do(t, http.MethodPost, "/api/findings/f/actions/"+tc.verb, "", cookies...)
			if code != tc.want || !strings.Contains(body, tc.wantMsg) {
				t.Fatalf("status = %d (%s), want %d containing %q", code, body, tc.want, tc.wantMsg)
			}
			got := getFinding(t, mustClient(g.s), "f")
			applied := got.Spec.Approval != nil || got.Spec.Expedite != nil
			if applied != tc.applied {
				t.Fatalf("applied = %v, want %v (spec %+v)", applied, tc.applied, got.Spec)
			}
			// The audit trail names the patchy user, never the GitHub login.
			if got.Spec.Approval != nil && got.Spec.Approval.By != operator.Username {
				t.Errorf("approval by %q, want %q", got.Spec.Approval.By, operator.Username)
			}
		})
	}
}

func TestFindingVerbsFromMaintainedSet(t *testing.T) {
	shop := ghFinding("f-shop", "acme/shop", "gh", "github.com", v1alpha1.PhaseAwaitingApproval)
	docs := ghFinding("f-docs", "acme/docs", "gh", "github.com", v1alpha1.PhaseAwaitingApproval)
	g := newGHServer(t, maintainerGrants(), connectableIntegrationFor("gh", ""), shop, docs)
	g.users.maintained["gh"] = []ghuser.Repository{{Name: "Acme/Shop", Role: "maintain"}}
	cookie := g.link(t, "gh", "tok")

	verbsOf := func(name string) []string {
		t.Helper()
		code, body := g.do(t, http.MethodGet, "/api/findings/"+name, "", cookie)
		if code != http.StatusOK {
			t.Fatalf("detail = %d: %s", code, body)
		}
		var f Finding
		_ = json.Unmarshal([]byte(body), &f)
		return f.UserActions
	}

	// A cold cache never waits on GitHub: no scoped verbs yet, and a
	// background fill starts.
	if got := verbsOf("f-shop"); len(got) != 0 {
		t.Errorf("cold cache verbs = %v, want none", got)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, ok := g.s.maintained.get(maintainedKey("gh", 7), testClock); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background fill never landed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := verbsOf("f-shop"); !slices.Equal(got, []string{"approve", "expedite"}) {
		t.Errorf("maintained repo verbs = %v, want approve and expedite", got)
	}
	if got := verbsOf("f-docs"); len(got) != 0 {
		t.Errorf("unmaintained repo verbs = %v, want none", got)
	}

	// The set expires: past the TTL it is a miss again.
	if _, ok := g.s.maintained.get(maintainedKey("gh", 7), testClock.Add(maintainedTTL+time.Second)); ok {
		t.Error("the maintained set outlived its TTL")
	}

	// The list payload resolves per finding, and the user carries the link.
	code, body := g.do(t, http.MethodGet, "/api/findings", "", cookie)
	if code != http.StatusOK {
		t.Fatalf("list = %d: %s", code, body)
	}
	var page FindingsPage
	_ = json.Unmarshal([]byte(body), &page)
	for _, f := range page.Findings {
		want := 0
		if f.Name == "f-shop" {
			want = 2
		}
		if len(f.UserActions) != want {
			t.Errorf("%s userActions = %v", f.Name, f.UserActions)
		}
	}
	if u := page.User; u == nil || len(u.GitHub) != 1 || !u.GitHub[0].Connected || u.GitHub[0].Login != "octocat" ||
		u.GitHub[0].Host != "github.com" || len(u.MaintainedActions) != 3 {
		t.Errorf("user = %+v", page.User)
	}
}

func TestGitHubRepositoriesRoute(t *testing.T) {
	g := newGHServer(t, maintainerGrants(), connectableIntegrationFor("gh", "https://ghes.acme.test"),
		scanningIntegration("plain"))
	g.users.maintained["gh"] = []ghuser.Repository{{Name: "acme/shop", Role: "admin"}}

	code, body := g.do(t, http.MethodGet, "/api/github/repositories?integration=gh", "", g.link(t, "gh", "tok"))
	if code != http.StatusOK {
		t.Fatalf("status = %d: %s", code, body)
	}
	var out GitHubRepositoriesPayload
	_ = json.Unmarshal([]byte(body), &out)
	if out.Login != "octocat" || len(out.Repositories) != 1 || out.Repositories[0].Name != "acme/shop" {
		t.Errorf("payload = %+v", out)
	}
	if _, ok := g.s.maintained.get(maintainedKey("gh", 7), testClock); !ok {
		t.Error("the route did not fill the maintained cache")
	}

	if code, _ := g.do(t, http.MethodGet, "/api/github/repositories?integration=gh", ""); code != http.StatusConflict {
		t.Errorf("not connected = %d, want 409", code)
	}
	if code, _ := g.do(t, http.MethodGet, "/api/github/repositories?integration=plain", "",
		g.link(t, "plain", "tok")); code != http.StatusNotFound {
		t.Errorf("non-connectable integration = %d, want 404", code)
	}
	// A rejected token whose refresh also fails drops the link.
	g.users.reject["dead"] = true
	if code, body := g.do(t, http.MethodGet, "/api/github/repositories?integration=gh", "",
		g.link(t, "gh", "dead")); code != http.StatusForbidden || !strings.Contains(body, "Reconnect") {
		t.Errorf("dead link = %d (%s), want 403 Reconnect", code, body)
	}
}

func TestConnectableIntegrations(t *testing.T) {
	notReady := connectableIntegrationFor("gh-c", "")
	notReady.Status.Conditions[0].Status = metav1.ConditionFalse
	suspended := connectableIntegrationFor("gh-d", "")
	suspended.Spec.Suspend = true
	g := newGHServer(t, maintainerGrants(), connectableIntegrationFor("gh-b", "https://ghes.acme.test/api/v3"),
		connectableIntegrationFor("gh-a", ""), notReady, suspended, scanningIntegration("plain"))
	got := g.s.connectable(t.Context())
	if len(got) != 2 || got[0].Name != "gh-a" || got[1].Name != "gh-b" || got[1].Host() != "ghes.acme.test" ||
		got[0].ClientID != "Iv1.gh-a" {
		t.Errorf("connectable = %+v, want gh-a and gh-b in name order", got)
	}
	// With linking off there is nothing to connect.
	if out := repoServer(t, allGrants(), nil, connectableIntegrationFor("gh", "")).connectable(t.Context()); out != nil {
		t.Errorf("linking off: connectable = %+v", out)
	}
}
