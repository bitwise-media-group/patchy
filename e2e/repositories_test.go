// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	authnv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/e2e/fakegithub"
)

// repositoriesPayload mirrors the slice of GET /api/repositories this test
// asserts (the full contract is unit-tested in internal/web).
type repositoriesPayload struct {
	AccessCheck  bool `json:"accessCheck"`
	CanBackfill  bool `json:"canBackfill"`
	Repositories []struct {
		Name     string         `json:"name"`
		Findings int            `json:"findings"`
		Phases   map[string]int `json:"phases"`
	} `json:"repositories"`
}

type repositoryAccessPayload struct {
	Available    bool   `json:"available"`
	Error        string `json:"error"`
	Repositories []struct {
		Name                string `json:"name"`
		Skipped             string `json:"skipped"`
		BackfillIntegration string `json:"backfillIntegration"`
		Integrations        []struct {
			Name   string `json:"name"`
			Alerts struct {
				CodeScanning *struct {
					Status string `json:"status"`
				} `json:"codeScanning"`
			} `json:"alerts"`
		} `json:"integrations"`
		Forge *struct {
			Status   string `json:"status"`
			Contents string `json:"contents"`
		} `json:"forge"`
	} `json:"repositories"`
}

// TestRepositoriesPage drives the repositories page's backend across both
// real binaries: the status server asks the integration-controller's
// internal endpoint, authenticated by a real projected-style token that the
// envtest API server reviews, whether patchy can read a never-seen
// repository's alerts; a one-repository BackfillRequest is created on the
// Integration that can; its alerts become findings; and the cooldown then
// refuses a second backfill of the same repository.
func TestRepositoriesPage(t *testing.T) {
	cl := startCluster(t)
	gh := fakegithub.New()
	t.Cleanup(gh.Close)
	cl.githubCredentials(t, gh.URL)

	// acme/fresh has an alert no webhook ever delivered; the credential may
	// not read acme/locked's alerts at all.
	gh.SeedAlert("acme", "fresh", 11)
	gh.ForbidAlerts("acme", "locked")

	base := startRepositoriesStack(t, cl)
	repositoriesCheckUnseen(t, base)
	repositoriesCheckBackfill(t, cl, base)
}

// startRepositoriesStack runs the integration-controller with its internal
// endpoint and a status server wired to it, checks the endpoint admits only
// the status server's token, and returns the status server's base URL.
func startRepositoriesStack(t *testing.T, cl *cluster) string {
	t.Helper()
	cs := clientset(t, cl)
	statusToken := serviceAccountToken(t, cs, "patchy-status-server")
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte(statusToken), 0o600); err != nil {
		t.Fatal(err)
	}

	internal := "http://" + fmt.Sprintf("127.0.0.1:%d", freePort(t))
	cl.controller(t, "integration-controller",
		"--listen-addr", fmt.Sprintf("127.0.0.1:%d", freePort(t)),
		"--internal-addr", strings.TrimPrefix(internal, "http://"),
		"--status-server-service-account", "patchy-status-server")
	waitListening(t, internal+"/")
	// A backfill request filed before the Integration is Ready is held for
	// minutes; the page's backfill below needs it Ready.
	waitIntegrationReady(t, cl, "github")

	// Only the status server's ServiceAccount, with the right audience, may
	// call the internal endpoint.
	foreign := serviceAccountToken(t, cs, "someone-else")
	for name, tok := range map[string]string{"no token": "", "foreign identity": foreign} {
		if code := postInternal(t, internal, tok); code != http.StatusUnauthorized {
			t.Errorf("internal endpoint with %s = %d, want 401", name, code)
		}
	}
	if code := postInternal(t, internal, statusToken); code != http.StatusOK {
		t.Fatalf("internal endpoint with the status server's token = %d, want 200", code)
	}

	authCfg := filepath.Join(t.TempDir(), "auth.yaml")
	if err := os.WriteFile(authCfg, []byte("mode: none\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	listen := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	cl.controller(t, "status-server", "--listen-addr", listen, "--auth-config", authCfg,
		"--integration-controller-url", internal,
		"--integration-controller-token-file", tokenFile)
	return "http://" + listen
}

// repositoriesCheckUnseen asserts the page's view of two never-seen
// repositories: no counts, one readable and one forbidden.
func repositoriesCheckUnseen(t *testing.T, base string) {
	t.Helper()
	repos := "repo=acme%2Ffresh&repo=acme%2Flocked"
	var summary repositoriesPayload
	getJSON(t, base+"/api/repositories?"+repos, &summary)
	if !summary.AccessCheck || !summary.CanBackfill || len(summary.Repositories) != 2 {
		t.Fatalf("summary = %+v", summary)
	}
	for _, r := range summary.Repositories {
		if r.Findings != 0 {
			t.Fatalf("%s findings = %d before any backfill, want 0", r.Name, r.Findings)
		}
	}

	var access repositoryAccessPayload
	getJSON(t, base+"/api/repositories/access?"+repos, &access)
	if !access.Available || access.Error != "" || len(access.Repositories) != 2 {
		t.Fatalf("access = %+v", access)
	}
	fresh, locked := access.Repositories[0], access.Repositories[1]
	if fresh.BackfillIntegration != "github" {
		t.Errorf("acme/fresh backfill integration = %q, want github", fresh.BackfillIntegration)
	}
	// The e2e Forge holds a PAT, whose scopes patchy cannot see.
	if fresh.Forge == nil || fresh.Forge.Status != "ok" || fresh.Forge.Contents != "unknown" {
		t.Errorf("acme/fresh forge = %+v, want ok/unknown", fresh.Forge)
	}
	if locked.BackfillIntegration != "" || len(locked.Integrations) != 1 ||
		locked.Integrations[0].Alerts.CodeScanning == nil ||
		locked.Integrations[0].Alerts.CodeScanning.Status != "forbidden" {
		t.Errorf("acme/locked = %+v, want forbidden and not backfillable", locked)
	}
}

// repositoriesCheckBackfill backfills the readable repository, waits for
// its alert to become a finding, and checks the cooldown then refuses
// another backfill of it.
func repositoriesCheckBackfill(t *testing.T, cl *cluster, base string) {
	t.Helper()
	ctx := context.Background()
	if code, body := postBackfillRepo(t, base, "acme/locked"); code != http.StatusConflict {
		t.Errorf("backfill acme/locked = %d (%s), want 409", code, body)
	}
	code, res := postBackfillJSON(t, base, map[string]any{"repository": "acme/fresh"})
	if code != http.StatusOK || res.Outcome != "requested" || res.Integration != "github" || res.Request == "" {
		t.Fatalf("backfill acme/fresh = %d %+v, want 200 requested on github", code, res)
	}
	// The page created a BackfillRequest on the granted path (mode none
	// holds every grant), naming exactly the repository.
	bfr := getBackfillRequest(t, cl, res.Request)
	if !slices.Equal(bfr.Spec.Repositories, []string{"acme/fresh"}) || bfr.Spec.Via != "" ||
		bfr.Spec.IntegrationRef.Name != "github" || bfr.Spec.RequestedBy == "" {
		t.Fatalf("backfill request spec = %+v, want acme/fresh on github by the grant", bfr.Spec)
	}

	// The controller walks the request: the never-delivered alert becomes a
	// finding, and the page counts it.
	eventually(t, "the repositories page to count the backfilled finding", func() bool {
		var got repositoriesPayload
		getJSON(t, base+"/api/repositories?repo=acme%2Ffresh", &got)
		return len(got.Repositories) == 1 && got.Repositories[0].Findings == 1
	})
	var list v1alpha1.FindingList
	if err := cl.client.List(ctx, &list, client.InNamespace(namespace)); err != nil || len(list.Items) != 1 ||
		list.Items[0].Spec.Repository == nil || list.Items[0].Spec.Repository.Name != "acme/fresh" {
		t.Fatalf("findings = %+v (err %v), want one in acme/fresh", list.Items, err)
	}
	if st := waitBackfillPhase(t, cl, res.Request, v1alpha1.BackfillComplete); st.Ingested != 1 {
		t.Errorf("backfill request status = %+v, want 1 ingested", st)
	}

	// A known repository may be backfilled again, but not inside the
	// cooldown after a walk that named it.
	if code, body := postBackfillRepo(t, base, "acme/fresh"); code != http.StatusTooManyRequests {
		t.Errorf("second backfill of acme/fresh = %d (%s), want 429 inside the cooldown", code, body)
	}
	var access repositoryAccessPayload
	getJSON(t, base+"/api/repositories/access?repo=acme%2Ffresh", &access)
	if len(access.Repositories) != 1 || access.Repositories[0].Skipped != "known" {
		t.Errorf("access after backfill = %+v, want skipped known", access.Repositories)
	}
}

func clientset(t *testing.T, cl *cluster) kubernetes.Interface {
	t.Helper()
	cfg, err := clientcmd.BuildConfigFromFlags("", cl.kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

// serviceAccountToken creates the ServiceAccount and mints it a token for
// the integration-controller's audience, as the kubelet's projected volume
// would.
func serviceAccountToken(t *testing.T, cs kubernetes.Interface, name string) string {
	t.Helper()
	ctx := context.Background()
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
	if _, err := cs.CoreV1().ServiceAccounts(namespace).Create(ctx, sa, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create service account %s: %v", name, err)
	}
	expiry := int64(3600)
	tr, err := cs.CoreV1().ServiceAccounts(namespace).CreateToken(ctx, name, &authnv1.TokenRequest{
		Spec: authnv1.TokenRequestSpec{
			Audiences:         []string{"patchy-integration-controller"},
			ExpirationSeconds: &expiry,
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("token for %s: %v", name, err)
	}
	return tr.Status.Token
}

// waitListening waits for any HTTP answer at url.
func waitListening(t *testing.T, url string) {
	t.Helper()
	eventually(t, "the listener at "+url, func() bool {
		resp, err := http.Get(url)
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return true
	})
}

func postInternal(t *testing.T, base, token string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, base+"/internal/v1/repository-access",
		strings.NewReader(`{"repositories":["acme/fresh"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("X-Patchy-Caller-Token", token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST internal: %v", err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func getJSON(t *testing.T, u string, v any) {
	t.Helper()
	resp, err := http.Get(u)
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

func postBackfillRepo(t *testing.T, base, repo string) (int, string) {
	t.Helper()
	return postJSON(t, http.DefaultClient, base+"/api/repositories/backfill", map[string]any{"repository": repo})
}

// repositoryBackfillResult mirrors POST /api/repositories/backfill's
// answer.
type repositoryBackfillResult struct {
	Integration string `json:"integration"`
	Request     string `json:"request"`
	Outcome     string `json:"outcome"`
}

// postBackfillJSON posts a one-repository backfill with the default client
// and decodes a 200 answer.
func postBackfillJSON(t *testing.T, base string, body map[string]any) (int, repositoryBackfillResult) {
	t.Helper()
	return postBackfillAs(t, http.DefaultClient, base, body)
}

// postBackfillAs posts a one-repository backfill with c (its cookie jar
// carries any GitHub link) and decodes a 200 answer.
func postBackfillAs(
	t *testing.T, c *http.Client, base string, body map[string]any,
) (int, repositoryBackfillResult) {
	t.Helper()
	code, raw := postJSON(t, c, base+"/api/repositories/backfill", body)
	var res repositoryBackfillResult
	if code == http.StatusOK {
		if err := json.Unmarshal([]byte(raw), &res); err != nil {
			t.Fatalf("decode backfill answer %q: %v", raw, err)
		}
	}
	return code, res
}

// postJSON posts body as JSON with c and returns the status and trimmed
// response body.
func postJSON(t *testing.T, c *http.Client, u string, body any) (int, string) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	resp, err := c.Post(u, "application/json", rd)
	if err != nil {
		t.Fatalf("POST %s: %v", u, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(raw))
}
