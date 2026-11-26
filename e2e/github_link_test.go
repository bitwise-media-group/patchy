// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package e2e

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/e2e/fakegithub"
)

// The App github Integration the maintainer scenarios link through.
const (
	appIntegration  = "github-app"
	appID           = 4242
	appClientID     = "Iv23e2eclient"
	appClientSecret = "e2e-client-secret"
	linkCookie      = "patchy-ghlink-" + appIntegration
	maintainerLogin = "octo-maintainer"
)

// The status-page identities, one anonymous-mode status server each, and
// the only grants each holds.
const (
	maintainerUser = "maintainer@acme.test" // the three *-maintained verbs
	viewerUser     = "viewer@acme.test"     // view findings only
	operatorUser   = "operator@acme.test"   // the namespace-wide backfill verb
)

// TestGitHubMaintainer drives the "Connect GitHub" link and the scoped
// maintainer grants across the real integration-controller and status
// servers, with the fake GitHub as the App, the OAuth server and the user
// API:
//
//   - a maintainer-only identity connects GitHub (authorize, the fake's
//     consent, the callback with PKCE and the CSRF double-submit), lists
//     the repositories they maintain, survives an access-token expiry by a
//     single-use refresh, backfills one of them (a maintained
//     BackfillRequest walked through the App's installation fan-out), and
//     is held to the cooldown on a second backfill;
//   - the same identity approves and expedites a finding on a maintained
//     repository, and is refused on one where their GitHub role is write;
//   - a viewer files a backfill request that waits for an operator, who
//     approves it (walked into findings), and another the operator denies.
func TestGitHubMaintainer(t *testing.T) {
	cl := startCluster(t)
	gh := fakegithub.New()
	t.Cleanup(gh.Close)
	cl.githubAppCredentials(t, gh)

	// The estate: alerts no webhook delivered, on repositories with
	// different roles for the maintainer.
	gh.SeedAlert("acme", "shop", 21)
	gh.SeedAlert("acme", "shop", 22)
	gh.SeedAlert("acme", "web", 31)
	gh.SeedAlert("acme", "api", 41)
	gh.SetUserRole(maintainerLogin, "acme/shop", fakegithub.RoleMaintain)
	gh.SetUserRole(maintainerLogin, "acme/lib", fakegithub.RoleAdmin)
	gh.SetUserRole(maintainerLogin, "acme/web", fakegithub.RoleWrite)
	gh.SetUserRole(maintainerLogin, "acme/docs", fakegithub.RoleRead)

	grantRoles(t, cl)
	st := startMaintainerStack(t, cl)

	maintainer := cookieClient(t)
	connectGitHub(t, maintainer, st.maintainer, gh.URL)
	maintainerRepositories(t, gh, maintainer, st.maintainer)
	maintainerBackfill(t, cl, maintainer, st.maintainer)
	maintainerActions(t, cl, maintainer, st.maintainer)
	requestedBackfill(t, cl, st)
}

// maintainerStack is the running status servers' base URLs.
type maintainerStack struct {
	maintainer, viewer, operator string
}

// githubAppCredentials creates a GitHub App credential Secret (with the
// OAuth client secret) and an App github Integration that enables user
// authorization, registers the same App on the fake, and returns once the
// Integration is created.
func (cl *cluster) githubAppCredentials(t *testing.T, gh *fakegithub.Server) {
	t.Helper()
	ctx := context.Background()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	gh.RegisterApp(fakegithub.App{
		ID: appID, ClientID: appClientID, ClientSecret: appClientSecret,
		PublicKey: &key.PublicKey, Account: "acme",
	})
	if err := cl.client.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "patchy-github-app", Namespace: namespace},
		StringData: map[string]string{
			"appID":         fmt.Sprint(appID),
			"privateKey":    string(keyPEM),
			"webhookSecret": webhookSecret,
			"clientSecret":  appClientSecret,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := cl.client.Create(ctx, &v1alpha1.Integration{
		ObjectMeta: metav1.ObjectMeta{Name: appIntegration, Namespace: namespace},
		Spec: v1alpha1.IntegrationSpec{
			Provider:  v1alpha1.IntegrationProviderGitHub,
			SecretRef: &v1alpha1.LocalSecretReference{Name: "patchy-github-app"},
			GitHub: &v1alpha1.GitHubIntegration{
				BaseURL:              gh.URL,
				CodeScanningAlerts:   &v1alpha1.GitHubCodeScanningAlerts{Enabled: true},
				SecretScanningAlerts: &v1alpha1.GitHubSecretScanningAlerts{Enabled: true},
				UserAuthorization:    &v1alpha1.GitHubUserAuthorization{ClientID: appClientID},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
}

// grantRoles binds each status-page identity to exactly its grants.
func grantRoles(t *testing.T, cl *cluster) {
	t.Helper()
	ctx := context.Background()
	rbac := clientset(t, cl).RbacV1()
	rule := func(resource string, verbs ...string) rbacv1.PolicyRule {
		return rbacv1.PolicyRule{
			APIGroups: []string{v1alpha1.GroupVersion.Group}, Resources: []string{resource}, Verbs: verbs,
		}
	}
	for user, rules := range map[string][]rbacv1.PolicyRule{
		maintainerUser: {
			rule("findings", "get", "list", "watch", "approve-maintained", "expedite-maintained"),
			rule("findingrollups", "get"),
			rule("integrations", "backfill-maintained"),
		},
		viewerUser: {rule("findings", "get", "list", "watch"), rule("findingrollups", "get")},
		operatorUser: {
			rule("findings", "get", "list", "watch"),
			rule("integrations", "get", "backfill"),
		},
	} {
		name := strings.Split(user, "@")[0]
		if _, err := rbac.Roles(namespace).Create(ctx, &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Rules:      rules,
		}, metav1.CreateOptions{}); err != nil {
			t.Fatalf("create role %s: %v", name, err)
		}
		if _, err := rbac.RoleBindings(namespace).Create(ctx, &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: name},
			Subjects:   []rbacv1.Subject{{APIGroup: rbacv1.GroupName, Kind: rbacv1.UserKind, Name: user}},
		}, metav1.CreateOptions{}); err != nil {
			t.Fatalf("create role binding %s: %v", name, err)
		}
	}
}

// startMaintainerStack runs the integration-controller with its internal
// endpoints, waits for the App Integration's user authorization to verify
// against GET /app, and starts one anonymous-mode status server per
// identity. Only the maintainer's has a link key, so only it can connect
// GitHub.
func startMaintainerStack(t *testing.T, cl *cluster) maintainerStack {
	t.Helper()
	tokenFile := filepath.Join(t.TempDir(), "token")
	token := serviceAccountToken(t, clientset(t, cl), "patchy-status-server")
	if err := os.WriteFile(tokenFile, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	internal := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	cl.controller(t, "integration-controller",
		"--listen-addr", fmt.Sprintf("127.0.0.1:%d", freePort(t)),
		"--internal-addr", internal,
		"--status-server-service-account", "patchy-status-server")
	waitListening(t, "http://"+internal+"/")

	// The reconciler checked the configured client ID against the App's
	// own (GET /app), so the condition is a verified Configured.
	eventually(t, "the App Integration's user authorization to be ready", func() bool {
		var integ v1alpha1.Integration
		key := types.NamespacedName{Namespace: namespace, Name: appIntegration}
		if err := cl.client.Get(context.Background(), key, &integ); err != nil {
			return false
		}
		c := meta.FindStatusCondition(integ.Status.Conditions, v1alpha1.ConditionUserAuthorizationReady)
		return c != nil && c.Status == metav1.ConditionTrue && c.Reason == "Configured" &&
			meta.IsStatusConditionTrue(integ.Status.Conditions, v1alpha1.ConditionReady)
	})

	linkKey := filepath.Join(t.TempDir(), "link-key")
	if err := os.WriteFile(linkKey, []byte(strings.Repeat("e2e-link-key-", 4)), 0o600); err != nil {
		t.Fatal(err)
	}
	start := func(user, extra string) string {
		cfg := filepath.Join(t.TempDir(), "auth.yaml")
		body := fmt.Sprintf("mode: anonymous\ninsecure: true\n%sanonymous:\n  username: %s\n", extra, user)
		if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		listen := fmt.Sprintf("127.0.0.1:%d", freePort(t))
		cl.controller(t, "status-server", "--listen-addr", listen, "--auth-config", cfg,
			"--integration-controller-url", "http://"+internal,
			"--integration-controller-token-file", tokenFile)
		return "http://" + listen
	}
	return maintainerStack{
		maintainer: start(maintainerUser, "linkKeyFile: "+linkKey+"\n"),
		viewer:     start(viewerUser, ""),
		operator:   start(operatorUser, ""),
	}
}

// cookieClient is a browser stand-in: a client with its own cookie jar.
func cookieClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar}
}

// noFollow is c, sharing its jar, but stopping at every redirect.
func noFollow(c *http.Client) *http.Client {
	return &http.Client{Jar: c.Jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

// redirect GETs u without following and returns the redirect target.
func redirect(t *testing.T, c *http.Client, u string, want int) *url.URL {
	t.Helper()
	resp, err := noFollow(c).Get(u)
	if err != nil {
		t.Fatalf("GET %s: %v", u, err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != want {
		t.Fatalf("GET %s = %d (%s), want %d", u, resp.StatusCode, body, want)
	}
	loc, err := resp.Location()
	if err != nil {
		t.Fatalf("GET %s: no Location: %v", u, err)
	}
	return loc
}

// hasCookie reports whether c's jar holds a non-empty cookie named name for
// base.
func hasCookie(t *testing.T, c *http.Client, base, name string) bool {
	t.Helper()
	u, err := url.Parse(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	for _, ck := range c.Jar.Cookies(u) {
		if ck.Name == name && ck.Value != "" {
			return true
		}
	}
	return false
}

// connectGitHub walks the browser through "Connect GitHub": the status
// server's authorize redirect (PKCE challenge, state), the fake's consent,
// and the callback, which must exchange the code through the
// integration-controller and set the sealed link cookie. A replayed
// callback from a browser without the state cookie is refused.
func connectGitHub(t *testing.T, browser *http.Client, base, ghURL string) {
	t.Helper()
	authorize := redirect(t, browser,
		base+"/oauth2/github/"+appIntegration+"/authorize?originalPath=%2Frepositories", http.StatusFound)
	q := authorize.Query()
	callbackURL := base + "/oauth2/github/" + appIntegration + "/callback"
	if got := authorize.Scheme + "://" + authorize.Host + authorize.Path; got != ghURL+"/login/oauth/authorize" {
		t.Fatalf("authorize redirect = %s, want the fake's web root /login/oauth/authorize", authorize)
	}
	if q.Get("client_id") != appClientID || q.Get("redirect_uri") != callbackURL ||
		q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("state") == "" {
		t.Fatalf("authorize query = %v", q)
	}
	if !hasCookie(t, browser, base+"/oauth2/github", "patchy-github-state") {
		t.Fatal("the authorize step set no state cookie")
	}

	// The fake's consent page approves for the maintainer and sends the
	// browser back with a code.
	q.Set("login", maintainerLogin)
	authorize.RawQuery = q.Encode()
	callback := redirect(t, browser, authorize.String(), http.StatusFound)
	if !strings.HasPrefix(callback.String(), callbackURL+"?") || callback.Query().Get("code") == "" {
		t.Fatalf("consent redirect = %s, want the callback with a code", callback)
	}

	// A browser without the state cookie cannot complete the flow, even
	// with the code and state in hand.
	attacker := cookieClient(t)
	redirect(t, attacker, callback.String(), http.StatusSeeOther)
	if hasCookie(t, attacker, base, linkCookie) || !hasCookie(t, attacker, base, "patchy-github-error") {
		t.Error("a callback without the state cookie linked an account, or reported no error")
	}

	back := redirect(t, browser, callback.String(), http.StatusSeeOther)
	if back.Path != "/repositories" {
		t.Errorf("callback redirected to %s, want the original path /repositories", back)
	}
	if !hasCookie(t, browser, base, linkCookie) {
		t.Fatal("the callback set no link cookie")
	}
	if hasCookie(t, browser, base, "patchy-github-error") {
		t.Error("the successful callback left an error cookie")
	}
}

// githubRepositoriesPayload mirrors GET /api/github/repositories.
type githubRepositoriesPayload struct {
	Integration  string `json:"integration"`
	Login        string `json:"login"`
	Repositories []struct {
		Name string `json:"name"`
		Role string `json:"role"`
	} `json:"repositories"`
}

// getAs GETs u with c and decodes a 200 JSON answer into v.
func getAs(t *testing.T, c *http.Client, u string, v any) {
	t.Helper()
	resp, err := c.Get(u)
	if err != nil {
		t.Fatalf("GET %s: %v", u, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET %s = %d: %s", u, resp.StatusCode, body)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("decode %s: %v", u, err)
	}
}

// maintainerRepositories lists the repositories the linked identity
// maintains (admin and maintain only, never write or read), then expires
// the access token on GitHub: the next listing refreshes it once, rotating
// the pair into the cookie, and the one after needs no refresh.
func maintainerRepositories(t *testing.T, gh *fakegithub.Server, browser *http.Client, base string) {
	t.Helper()
	u := base + "/api/github/repositories?integration=" + appIntegration
	check := func(when string) {
		t.Helper()
		var got githubRepositoriesPayload
		getAs(t, browser, u, &got)
		names := make([]string, 0, len(got.Repositories))
		for _, r := range got.Repositories {
			names = append(names, r.Name+":"+r.Role)
		}
		if got.Integration != appIntegration || got.Login != maintainerLogin ||
			!slices.Equal(names, []string{"acme/lib:admin", "acme/shop:maintain"}) {
			t.Errorf("%s: maintained repositories = %+v, want acme/lib admin and acme/shop maintain as @%s",
				when, got, maintainerLogin)
		}
	}
	check("after connecting")

	gh.ExpireUserTokens()
	check("after the access token expired")
	if n := gh.TokenRefreshes(); n != 1 {
		t.Errorf("token refreshes after the expiry = %d, want 1", n)
	}
	check("after the refresh")
	if n := gh.TokenRefreshes(); n != 1 {
		t.Errorf("token refreshes after reusing the rotated pair = %d, want still 1", n)
	}

	// Without a link the listing is refused.
	resp, err := cookieClient(t).Get(u)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("repositories without a link = %d, want 409 (connect first)", resp.StatusCode)
	}
}

// maintainerBackfill backfills a maintained repository on the maintainer
// grant alone: the status server creates a maintained BackfillRequest
// carrying the GitHub identity, the integration-controller walks it through
// the App's installation into findings, and the cooldown then refuses a
// second backfill. A write-role repository and an unlinked browser are
// refused outright.
func maintainerBackfill(t *testing.T, cl *cluster, browser *http.Client, base string) {
	t.Helper()
	ctx := context.Background()
	maintainerBackfillRefused(t, browser, base)

	code, res := postBackfillAs(t, browser, base, map[string]any{"repository": "acme/shop"})
	if code != http.StatusOK || res.Outcome != "requested" || res.Integration != appIntegration {
		t.Fatalf("maintained backfill of acme/shop = %d %+v, want 200 requested on %s", code, res, appIntegration)
	}
	bfr := getBackfillRequest(t, cl, res.Request)
	if bfr.Spec.Via != v1alpha1.BackfillViaMaintained || bfr.Spec.RequestedBy != maintainerUser ||
		bfr.Spec.GitHub == nil || bfr.Spec.GitHub.Login != maintainerLogin ||
		bfr.Spec.GitHub.Role != fakegithub.RoleMaintain ||
		!slices.Equal(bfr.Spec.Repositories, []string{"acme/shop"}) {
		t.Fatalf("maintained request spec = %+v (github %+v)", bfr.Spec, bfr.Spec.GitHub)
	}
	st := waitBackfillPhase(t, cl, res.Request, v1alpha1.BackfillComplete)
	if st.Listed != 2 || st.Ingested != 2 || st.Error != "" {
		t.Errorf("maintained request status = %+v, want listed 2 ingested 2", st)
	}
	shop := backfilledFindings(t, cl, "acme/shop")
	if len(shop) != 1 || len(shop[0].Spec.Alerts) != 2 || shop[0].Spec.IntegrationRef.Name != appIntegration {
		t.Fatalf("acme/shop findings = %+v, want one family of 2 alerts on %s", shop, appIntegration)
	}

	// The walk finished inside the cooldown: another is refused.
	if code, body := postJSON(t, browser, base+"/api/repositories/backfill",
		map[string]any{"repository": "acme/shop"}); code != http.StatusTooManyRequests {
		t.Errorf("second maintained backfill of acme/shop = %d (%s), want 429", code, body)
	}
	var reqs v1alpha1.BackfillRequestList
	if err := cl.client.List(ctx, &reqs, client.InNamespace(namespace)); err != nil || len(reqs.Items) != 1 {
		t.Errorf("backfill requests = %d (err %v), want only the one walked", len(reqs.Items), err)
	}
}

// maintainerBackfillRefused checks the maintained backfill refuses an
// unlinked browser and a repository where the linked role is write.
func maintainerBackfillRefused(t *testing.T, browser *http.Client, base string) {
	t.Helper()
	if code, body := postJSON(t, cookieClient(t), base+"/api/repositories/backfill",
		map[string]any{"repository": "acme/shop"}); code != http.StatusForbidden ||
		!strings.Contains(body, "Connect GitHub") {
		t.Errorf("maintained backfill without a link = %d (%s), want 403 connect GitHub", code, body)
	}
	if code, body := postJSON(t, browser, base+"/api/repositories/backfill",
		map[string]any{"repository": "acme/web"}); code != http.StatusForbidden || !strings.Contains(body, "write") {
		t.Errorf("maintained backfill of the write-role acme/web = %d (%s), want 403 naming the role", code, body)
	}
}

// backfilledFindings returns the findings in repo that carry alerts (the
// fabricated ones carry none).
func backfilledFindings(t *testing.T, cl *cluster, repo string) []v1alpha1.Finding {
	t.Helper()
	var list v1alpha1.FindingList
	if err := cl.client.List(context.Background(), &list, client.InNamespace(namespace)); err != nil {
		t.Fatal(err)
	}
	var out []v1alpha1.Finding
	for _, f := range list.Items {
		if f.Spec.Repository != nil && f.Spec.Repository.Name == repo && len(f.Spec.Alerts) > 0 {
			out = append(out, f)
		}
	}
	return out
}

// fabricateAwaiting creates a finding on the App Integration in repo,
// waiting for approval.
func fabricateAwaiting(t *testing.T, cl *cluster, name, repo, ghURL string) {
	t.Helper()
	ctx := context.Background()
	now := metav1.Now()
	fnd := &v1alpha1.Finding{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: v1alpha1.FindingSpec{
			IntegrationRef: v1alpha1.LocalObjectReference{Name: appIntegration},
			Source:         "ghas",
			Advisories:     []string{"CWE-79"},
			Title:          "Reflected cross-site scripting",
			Description:    "User-controlled input is written into the response unescaped.",
			Severity:       v1alpha1.LevelHigh,
			Repository: &v1alpha1.FindingRepository{
				Type: v1alpha1.RepositoryTypeGitHub, URL: ghURL + "/" + repo, Name: repo, DefaultBranch: "main",
			},
		},
	}
	if err := cl.client.Create(ctx, fnd); err != nil {
		t.Fatal(err)
	}
	fnd.Status.Phase = v1alpha1.PhaseAwaitingApproval
	fnd.Status.PhaseTimes = []v1alpha1.PhaseTime{{Phase: v1alpha1.PhaseAwaitingApproval, At: now}}
	if err := cl.client.Status().Update(ctx, fnd); err != nil {
		t.Fatal(err)
	}
}

func getFinding(t *testing.T, cl *cluster, name string) *v1alpha1.Finding {
	t.Helper()
	var f v1alpha1.Finding
	if err := cl.client.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: name}, &f); err != nil {
		t.Fatal(err)
	}
	return &f
}

// maintainerActions approves and expedites with the scoped grants: allowed
// on a maintained repository through the link, refused on a write-role
// repository, refused without a link, and never extended to verbs with no
// scoped grant.
func maintainerActions(t *testing.T, cl *cluster, browser *http.Client, base string) {
	t.Helper()
	var integ v1alpha1.Integration
	if err := cl.client.Get(context.Background(),
		types.NamespacedName{Namespace: namespace, Name: appIntegration}, &integ); err != nil {
		t.Fatal(err)
	}
	ghURL := integ.Spec.GitHub.BaseURL
	fabricateAwaiting(t, cl, "finding-shop-awaiting", "acme/shop", ghURL)
	fabricateAwaiting(t, cl, "finding-web-awaiting", "acme/web", ghURL)
	act := func(c *http.Client, name, verb string) (int, string) {
		t.Helper()
		return postJSON(t, c, fmt.Sprintf("%s/api/findings/%s/actions/%s", base, name, verb), nil)
	}

	if code, body := act(cookieClient(t), "finding-shop-awaiting", "approve"); code != http.StatusForbidden ||
		!strings.Contains(body, "Connect GitHub") {
		t.Errorf("approve without a link = %d (%s), want 403 connect GitHub", code, body)
	}
	for _, verb := range []string{"approve", "expedite"} {
		if code, body := act(browser, "finding-shop-awaiting", verb); code != http.StatusOK {
			t.Errorf("%s on maintained acme/shop = %d (%s), want 200", verb, code, body)
		}
		if code, body := act(browser, "finding-web-awaiting", verb); code != http.StatusForbidden ||
			!strings.Contains(body, "write") {
			t.Errorf("%s on write-role acme/web = %d (%s), want 403 naming the role", verb, code, body)
		}
	}
	// No scoped grant stands in for suspend.
	if code, body := act(browser, "finding-shop-awaiting", "suspend"); code != http.StatusForbidden {
		t.Errorf("suspend with only maintainer grants = %d (%s), want 403", code, body)
	}

	shop := getFinding(t, cl, "finding-shop-awaiting")
	if shop.Spec.Approval == nil || shop.Spec.Approval.By != maintainerUser {
		t.Errorf("acme/shop approval = %+v, want by %s", shop.Spec.Approval, maintainerUser)
	}
	if shop.Spec.Expedite == nil || shop.Spec.Suspend {
		t.Errorf("acme/shop spec = expedite %+v suspend %v, want expedited and not suspended",
			shop.Spec.Expedite, shop.Spec.Suspend)
	}
	if web := getFinding(t, cl, "finding-web-awaiting"); web.Spec.Approval != nil || web.Spec.Expedite != nil {
		t.Errorf("acme/web spec = approval %+v expedite %+v, want untouched", web.Spec.Approval, web.Spec.Expedite)
	}
}

// backfillRequestsPayload mirrors the slice of GET /api/backfill-requests
// asserted here.
type backfillRequestsPayload struct {
	CanDecide bool                  `json:"canDecide"`
	Requests  []backfillRequestWire `json:"requests"`
}

// backfillRequestWire is one listed request.
type backfillRequestWire struct {
	Name  string `json:"name"`
	Via   string `json:"via"`
	Phase string `json:"phase"`
}

// requestedBackfill files backfills as a viewer, who holds no backfill
// grant: each waits in AwaitingApproval for an operator, who approves one
// (it is then walked into findings) and denies the other (it never runs).
func requestedBackfill(t *testing.T, cl *cluster, st maintainerStack) {
	t.Helper()
	name := requestedFile(t, cl, st)
	requestedApprove(t, cl, st, name)
	requestedDeny(t, cl, st)
}

// requestedFile files a viewer's backfill request for acme/api and returns
// its name once it awaits approval.
func requestedFile(t *testing.T, cl *cluster, st maintainerStack) string {
	t.Helper()
	viewer := http.DefaultClient
	if code, body := postJSON(t, viewer, st.viewer+"/api/repositories/backfill",
		map[string]any{"repository": "acme/api"}); code != http.StatusForbidden {
		t.Errorf("viewer backfill without request = %d (%s), want 403", code, body)
	}
	code, res := postBackfillAs(t, viewer, st.viewer, map[string]any{"repository": "acme/api", "request": true})
	if code != http.StatusOK || res.Outcome != "awaiting-approval" || res.Integration != appIntegration {
		t.Fatalf("viewer backfill request = %d %+v, want 200 awaiting-approval", code, res)
	}
	if bfr := getBackfillRequest(t, cl, res.Request); bfr.Spec.Via != v1alpha1.BackfillViaRequested ||
		bfr.Spec.RequestedBy != viewerUser {
		t.Fatalf("requested backfill spec = %+v", bfr.Spec)
	}
	waitBackfillPhase(t, cl, res.Request, v1alpha1.BackfillAwaitingApproval)
	// The open request covers the repository; asking again files nothing.
	if code, again := postBackfillAs(t, viewer, st.viewer,
		map[string]any{"repository": "acme/api", "request": true}); code != http.StatusOK ||
		again.Outcome != "covered" || again.Request != res.Request {
		t.Errorf("second viewer request = %d %+v, want 200 covered by %s", code, again, res.Request)
	}
	if len(backfilledFindings(t, cl, "acme/api")) != 0 {
		t.Fatal("an undecided request was walked")
	}
	return res.Request
}

// requestedApprove checks only the operator may decide the request, then
// approves it and waits for the walk.
func requestedApprove(t *testing.T, cl *cluster, st maintainerStack, name string) {
	t.Helper()
	viewer, operator := http.DefaultClient, http.DefaultClient
	if code, body := postJSON(t, viewer, st.viewer+"/api/backfill-requests/"+name+"/approve",
		nil); code != http.StatusForbidden {
		t.Errorf("viewer approve = %d (%s), want 403", code, body)
	}
	var listed backfillRequestsPayload
	getAs(t, viewer, st.viewer+"/api/backfill-requests", &listed)
	if listed.CanDecide {
		t.Error("the viewer may decide backfill requests")
	}
	getAs(t, operator, st.operator+"/api/backfill-requests", &listed)
	if !listed.CanDecide || !slices.ContainsFunc(listed.Requests, func(r backfillRequestWire) bool {
		return r.Name == name && r.Via == "requested" && r.Phase == "AwaitingApproval"
	}) {
		t.Errorf("operator backfill requests = %+v, want decidable with %s awaiting approval", listed, name)
	}

	if code, body := postJSON(t, operator, st.operator+"/api/backfill-requests/"+name+"/approve",
		map[string]any{"reason": "in scope"}); code != http.StatusOK {
		t.Fatalf("operator approve = %d (%s), want 200", code, body)
	}
	if code, body := postJSON(t, operator, st.operator+"/api/backfill-requests/"+name+"/deny",
		nil); code != http.StatusConflict {
		t.Errorf("deciding twice = %d (%s), want 409", code, body)
	}
	bfr := getBackfillRequest(t, cl, name)
	if d := bfr.Spec.Decision; d == nil || d.Verdict != v1alpha1.BackfillApproved || d.By != operatorUser {
		t.Errorf("decision = %+v, want approved by %s", d, operatorUser)
	}
	if s := waitBackfillPhase(t, cl, name, v1alpha1.BackfillComplete); s.Ingested != 1 {
		t.Errorf("approved request status = %+v, want 1 ingested", s)
	}
	if got := backfilledFindings(t, cl, "acme/api"); len(got) != 1 {
		t.Errorf("acme/api findings after the approved walk = %d, want 1", len(got))
	}
}

// requestedDeny files a viewer's request the operator denies: it never
// runs.
func requestedDeny(t *testing.T, cl *cluster, st maintainerStack) {
	t.Helper()
	code, res := postBackfillAs(t, http.DefaultClient, st.viewer,
		map[string]any{"repository": "acme/web", "request": true})
	if code != http.StatusOK || res.Outcome != "awaiting-approval" {
		t.Fatalf("viewer request for acme/web = %d %+v", code, res)
	}
	waitBackfillPhase(t, cl, res.Request, v1alpha1.BackfillAwaitingApproval)
	if code, body := postJSON(t, http.DefaultClient, st.operator+"/api/backfill-requests/"+res.Request+"/deny",
		map[string]any{"reason": "not ours"}); code != http.StatusOK {
		t.Fatalf("operator deny = %d (%s), want 200", code, body)
	}
	if s := waitBackfillPhase(t, cl, res.Request, v1alpha1.BackfillDenied); s.Error != "not ours" ||
		s.Attempts != 0 || s.CompletedAt == nil {
		t.Errorf("denied request status = %+v, want the reason, no attempt, completed", s)
	}
	if got := backfilledFindings(t, cl, "acme/web"); len(got) != 0 {
		t.Errorf("acme/web findings after the denied request = %d, want 0", len(got))
	}
}
