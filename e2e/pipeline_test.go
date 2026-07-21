// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package e2e drives the real patchy binaries end to end: an envtest
// kube-apiserver carries the CRD state machine, a fake GitHub API stands in
// at the network boundary, and recorded webhook deliveries drive the
// pipeline. It is the check that the pieces fit together as shipped — no
// test doubles inside the binaries, only at the edges.
//
// What it deliberately does not cover: the agent Jobs never RUN (envtest has
// no kubelet), so the collect/apply leg — pod-log envelope events in,
// push + PR out — stays with the controller unit suites and the colima smoke
// test. Here the Jobs and their Secrets are asserted as created, shaped, and
// credential-less.
//
// Skipped without KUBEBUILDER_ASSETS (mise run e2e provisions it).
package e2e

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/e2e/fakegithub"
)

const (
	webhookSecret = "e2e-webhook-secret"
	namespace     = "patchy"
	agentsNS      = "patchy-agents"
)

// ---- binaries --------------------------------------------------------------

var (
	buildMu   sync.Mutex
	buildDir  string
	buildBins = map[string]string{}
)

// build compiles a controller binary from the product module once per test
// run. e2e is its own module, so the build runs in the parent directory.
func build(t *testing.T, name string) string {
	t.Helper()
	buildMu.Lock()
	defer buildMu.Unlock()
	if bin, ok := buildBins[name]; ok {
		return bin
	}
	if buildDir == "" {
		dir, err := os.MkdirTemp("", "patchy-e2e-bin")
		if err != nil {
			t.Fatal(err)
		}
		buildDir = dir
	}
	bin := filepath.Join(buildDir, name)
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/"+name)
	cmd.Dir = ".."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", name, err, out)
	}
	buildBins[name] = bin
	return bin
}

// ---- cluster ---------------------------------------------------------------

// cluster is one envtest API server with the patchy CRDs installed, plus the
// kubeconfig the controller binaries connect with.
type cluster struct {
	client     client.Client
	kubeconfig string
}

func startCluster(t *testing.T) *cluster {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS not set; run via `mise run e2e`")
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "deploy", "kustomize", "base", "crds")},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Errorf("stop envtest: %v", err)
		}
	})

	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		v1alpha1.AddToScheme, corev1.AddToScheme, batchv1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}

	// The binaries authenticate with a kubeconfig written from envtest's
	// admin credentials.
	kc := clientcmdapi.NewConfig()
	kc.Clusters["e2e"] = &clientcmdapi.Cluster{
		Server:                   cfg.Host,
		CertificateAuthorityData: cfg.CAData,
	}
	kc.AuthInfos["e2e"] = &clientcmdapi.AuthInfo{
		ClientCertificateData: cfg.CertData,
		ClientKeyData:         cfg.KeyData,
	}
	kc.Contexts["e2e"] = &clientcmdapi.Context{Cluster: "e2e", AuthInfo: "e2e"}
	kc.CurrentContext = "e2e"
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := clientcmd.WriteToFile(*kc, path); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}

	cl := &cluster{client: c, kubeconfig: path}
	for _, ns := range []string{namespace, agentsNS} {
		if err := c.Create(context.Background(), &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: ns},
		}); err != nil {
			t.Fatalf("create namespace %s: %v", ns, err)
		}
	}
	return cl
}

// githubCredentials creates the Secret plus the Integration and Forge custom
// resources that switch the pipeline on, all pointed at the fake GitHub.
func (cl *cluster) githubCredentials(t *testing.T, ghURL string) {
	t.Helper()
	ctx := context.Background()
	if err := cl.client.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "patchy-github", Namespace: namespace},
		StringData: map[string]string{
			"token":         "e2e-token",
			"webhookSecret": webhookSecret,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := cl.client.Create(ctx, &v1alpha1.Integration{
		ObjectMeta: metav1.ObjectMeta{Name: "github", Namespace: namespace},
		Spec: v1alpha1.IntegrationSpec{
			Provider:  v1alpha1.IntegrationProviderGitHub,
			SecretRef: v1alpha1.LocalSecretReference{Name: "patchy-github"},
			GitHub: &v1alpha1.GitHubIntegration{
				BaseURL:            ghURL,
				Issues:             &v1alpha1.GitHubIssues{Enabled: true, ApproveComment: "/approve"},
				CodeScanningAlerts: &v1alpha1.GitHubCodeScanningAlerts{Enabled: true},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := cl.client.Create(ctx, &v1alpha1.Forge{
		ObjectMeta: metav1.ObjectMeta{Name: "github", Namespace: namespace},
		Spec: v1alpha1.ForgeSpec{
			Provider:  v1alpha1.ForgeProviderGitHub,
			BaseURL:   ghURL,
			SecretRef: v1alpha1.LocalSecretReference{Name: "patchy-github"},
		},
	}); err != nil {
		t.Fatal(err)
	}
}

// ---- controllers -----------------------------------------------------------

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// controller starts a real controller binary against the envtest cluster and
// waits for its readiness probe. Returned is the health base URL; callers
// that need the webhook port pass --listen-addr themselves.
func (cl *cluster) controller(t *testing.T, name string, extra ...string) {
	t.Helper()
	health := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	args := append([]string{
		"serve",
		"--kubeconfig", cl.kubeconfig,
		"--namespace", namespace,
		"--health-addr", health,
		"--log-level", "info",
	}, extra...)

	cmd := exec.Command(build(t, name), args...)
	var logs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		if t.Failed() {
			t.Logf("%s logs:\n%s", name, logs.String())
		}
	})
	waitReady(t, "http://"+health+"/readyz")
}

func waitReady(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("controller never became ready at %s", url)
}

// ---- webhook deliveries ----------------------------------------------------

// deliver signs and posts a payload to the integration-controller's GitHub
// receiver, exactly as GitHub would.
func deliver(t *testing.T, url, event string, payload []byte) {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(webhookSecret))
	mac.Write(payload)

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-GitHub-Delivery", fmt.Sprintf("e2e-%s-%d", event, time.Now().UnixNano()))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("deliver %s: %v", event, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("deliver %s: status %d, want 202", event, resp.StatusCode)
	}
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join("fixtures", "webhooks", name))
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

// eventually polls until cond holds, failing with why after the deadline.
func eventually(t *testing.T, why string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", why)
}

// ---- the pipeline ----------------------------------------------------------

// TestPipeline drives the shipped binaries through the front half of the
// pipeline: scanner webhooks in, Finding CR out, accumulation, enhancement,
// issue projection, the investigation gate, the SHA-pinned artifact, and the
// credential-less agent Job — then a human close signal hands the finding
// off.
func TestPipeline(t *testing.T) {
	cl := startCluster(t)
	gh := fakegithub.New()
	t.Cleanup(gh.Close)
	cl.githubCredentials(t, gh.URL)
	ctx := context.Background()

	contextFile := filepath.Join(t.TempDir(), "context.yaml")
	if err := os.WriteFile(contextFile, []byte(
		"repos:\n    acme/shop:\n        owners: [octocat]\n        attributes:\n            system: storefront\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}

	listen := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	artifactPort := freePort(t)
	cl.controller(t, "integration-controller",
		"--listen-addr", listen, "--accumulation-window", "2s")
	cl.controller(t, "source-controller",
		"--artifact-addr", fmt.Sprintf("127.0.0.1:%d", artifactPort),
		"--artifact-base-url", fmt.Sprintf("http://127.0.0.1:%d", artifactPort),
		"--artifact-dir", t.TempDir())
	cl.controller(t, "context-controller", "--static-context-file", contextFile)
	cl.controller(t, "investigation-controller",
		"--finding-min-age", "1s", "--agent-image", "patchy/agent-runner:e2e")

	webhookURL := "http://" + listen + "/github/webhooks"

	// 1. The first CodeQL alert creates the Finding.
	deliver(t, webhookURL, "code_scanning_alert", fixture(t, "code_scanning_alert.created.json"))
	var fnd v1alpha1.Finding
	eventually(t, "the finding to be created", func() bool {
		var list v1alpha1.FindingList
		if err := cl.client.List(ctx, &list, client.InNamespace(namespace)); err != nil || len(list.Items) != 1 {
			return false
		}
		fnd = list.Items[0]
		return true
	})
	if fnd.Spec.Source != "ghas" || fnd.Spec.Severity != v1alpha1.LevelHigh {
		t.Errorf("finding spec = source %q severity %q, want ghas/high", fnd.Spec.Source, fnd.Spec.Severity)
	}
	if fnd.Spec.Repository == nil || !strings.HasSuffix(fnd.Spec.Repository.URL, "/acme/shop") {
		t.Errorf("finding repository = %+v, want acme/shop", fnd.Spec.Repository)
	}
	if !slices.Contains(fnd.Spec.Advisories, "CWE-79") {
		t.Errorf("advisories = %v, want CWE-79", fnd.Spec.Advisories)
	}

	// 2. A second alert of the same finding family folds in — still one
	//    Finding, now tracking both alerts.
	deliver(t, webhookURL, "code_scanning_alert", fixture(t, "code_scanning_alert.second.json"))
	eventually(t, "the second alert to accumulate", func() bool {
		var cur v1alpha1.Finding
		if err := cl.client.Get(ctx, client.ObjectKeyFromObject(&fnd), &cur); err != nil {
			return false
		}
		return len(cur.Spec.Alerts) == 2
	})
	var list v1alpha1.FindingList
	if err := cl.client.List(ctx, &list, client.InNamespace(namespace)); err != nil || len(list.Items) != 1 {
		t.Fatalf("findings = %d, want 1 (the second alert must accumulate, not create a sibling)", len(list.Items))
	}

	// 3. The context-controller enhances it (Opened → Enhanced).
	eventually(t, "the finding to be enhanced", func() bool {
		var cur v1alpha1.Finding
		if err := cl.client.Get(ctx, client.ObjectKeyFromObject(&fnd), &cur); err != nil {
			return false
		}
		return len(cur.Status.Enrichments) > 0 &&
			cur.Status.Phase != v1alpha1.PhaseOpened
	})
	var enhanced v1alpha1.Finding
	if err := cl.client.Get(ctx, client.ObjectKeyFromObject(&fnd), &enhanced); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(enhanced.Status.Owners, "octocat") {
		t.Errorf("owners = %v, want octocat from the static context", enhanced.Status.Owners)
	}

	// 4. The projection: one tracking issue in the fake GitHub, carrying the
	//    trimmed human-facing label vocabulary.
	eventually(t, "the tracking issue to be projected", func() bool {
		return len(gh.Issues()) == 1
	})
	issue := gh.Issues()[0]
	if want := "[ghas] CWE-79: Reflected cross-site scripting"; issue.Title != want {
		t.Errorf("issue title = %q, want %q", issue.Title, want)
	}
	labels := gh.LabelsOf(issue.Number)
	for _, want := range []string{"security-source: ghas", "security-advisory: CWE-79", "security-severity: high"} {
		if !slices.Contains(labels, want) {
			t.Errorf("labels %v missing %q", labels, want)
		}
	}
	for _, l := range labels {
		if strings.HasPrefix(l, "security-alert") || strings.HasPrefix(l, "security-accumulation") {
			t.Errorf("label %q is retired machine vocabulary; the CR is the state now", l)
		}
	}

	// 5. Accumulation closes (2s) and the gate picks the finding up (min age
	//    1s): Investigating, with the Repository pinned and served.
	eventually(t, "the finding to reach Investigating", func() bool {
		var cur v1alpha1.Finding
		if err := cl.client.Get(ctx, client.ObjectKeyFromObject(&fnd), &cur); err != nil {
			return false
		}
		return cur.Status.Phase == v1alpha1.PhaseInvestigating
	})

	var repo v1alpha1.Repository
	repoKey := types.NamespacedName{Namespace: namespace, Name: fnd.Name + "-src"}
	eventually(t, "the repository artifact to be served", func() bool {
		if err := cl.client.Get(ctx, repoKey, &repo); err != nil {
			return false
		}
		return repo.Status.Artifact != nil
	})
	if repo.Status.ResolvedSHA != fakegithub.HeadSHA {
		t.Errorf("resolvedSHA = %q, want the fake head %q", repo.Status.ResolvedSHA, fakegithub.HeadSHA)
	}
	resp, err := http.Get(repo.Status.Artifact.URL)
	if err != nil {
		t.Fatalf("fetch artifact: %v", err)
	}
	raw, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("fetch artifact: status %d, err %v", resp.StatusCode, err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != repo.Status.Artifact.Digest {
		t.Errorf("artifact digest = %s, want %s (what the Job pins)", got, repo.Status.Artifact.Digest)
	}

	// 6. The analysis Job and its per-Job Secret exist in the agents
	//    namespace — and are credential-less.
	var job batchv1.Job
	eventually(t, "the investigation job to be created", func() bool {
		var jobsList batchv1.JobList
		if err := cl.client.List(ctx, &jobsList, client.InNamespace(agentsNS)); err != nil || len(jobsList.Items) != 1 {
			return false
		}
		job = jobsList.Items[0]
		return true
	})
	if got := job.Labels[v1alpha1.LabelRunKind]; got != string(v1alpha1.RunKindInvestigation) {
		t.Errorf("job kind label = %q, want investigation", got)
	}
	if got := job.Labels[v1alpha1.LabelFinding]; got != fnd.Name {
		t.Errorf("job finding label = %q, want %s", got, fnd.Name)
	}
	initEnv := map[string]string{}
	for _, env := range job.Spec.Template.Spec.InitContainers[0].Env {
		initEnv[env.Name] = env.Value
	}
	if initEnv["PATCHY_ARTIFACT_URL"] != repo.Status.Artifact.URL {
		t.Errorf("job artifact URL = %q, want %q", initEnv["PATCHY_ARTIFACT_URL"], repo.Status.Artifact.URL)
	}
	var secret corev1.Secret
	if err := cl.client.Get(ctx, types.NamespacedName{Namespace: agentsNS, Name: job.Name}, &secret); err != nil {
		t.Fatalf("per-job secret: %v", err)
	}
	if _, ok := secret.Data["token"]; ok {
		t.Error("per-job secret carries a token — the agent flow must be credential-less")
	}
	if len(secret.Data["issue.md"]) == 0 {
		t.Error("per-job secret is missing the issue handoff")
	}

	// 7. A human closes the tracking issue: any non-terminal phase hands off.
	var tracked v1alpha1.Finding
	if err := cl.client.Get(ctx, client.ObjectKeyFromObject(&fnd), &tracked); err != nil {
		t.Fatal(err)
	}
	if tracked.Status.Tracking == nil || tracked.Status.Tracking.URL == "" {
		t.Fatal("finding carries no tracking link; the close signal has nothing to match")
	}
	closePayload, err := json.Marshal(map[string]any{
		"action": "closed",
		"issue": map[string]any{
			"number":   tracked.Status.Tracking.IssueNumber,
			"state":    "closed",
			"html_url": tracked.Status.Tracking.URL,
		},
		"repository": map[string]any{
			"name": "shop", "full_name": "acme/shop",
			"owner": map[string]any{"login": "acme"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	deliver(t, webhookURL, "issues", closePayload)
	eventually(t, "the closed issue to hand the finding off", func() bool {
		var cur v1alpha1.Finding
		if err := cl.client.Get(ctx, client.ObjectKeyFromObject(&fnd), &cur); err != nil {
			return false
		}
		return cur.Status.Phase == v1alpha1.PhaseHandedOff
	})
}

// ---- scheduling ------------------------------------------------------------

// fabricateFinding creates a Finding and drives its status to the given
// phase — the test stands in for the earlier pipeline stages.
func fabricateFinding(t *testing.T, cl *cluster, name string, severity v1alpha1.Level,
	repoURL string, mutate func(*v1alpha1.FindingStatus)) *v1alpha1.Finding {
	t.Helper()
	ctx := context.Background()
	fnd := &v1alpha1.Finding{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: v1alpha1.FindingSpec{
			IntegrationRef: v1alpha1.LocalObjectReference{Name: "github"},
			Source:         "ghas",
			Advisories:     []string{"CWE-79"},
			Title:          "Reflected cross-site scripting",
			Severity:       severity,
		},
	}
	if repoURL != "" {
		fnd.Spec.Repository = &v1alpha1.FindingRepository{
			Type: v1alpha1.RepositoryTypeGitHub, URL: repoURL, Name: "acme/shop", DefaultBranch: "main",
		}
	}
	if err := cl.client.Create(ctx, fnd); err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(&fnd.Status)
		if err := cl.client.Status().Update(ctx, fnd); err != nil {
			t.Fatal(err)
		}
	}
	return fnd
}

// TestRemediationPriorityOrder queues two findings of unequal priority behind
// a one-slot remediation scheduler and asserts the grant order: the critical
// finding's Remediation runs (its agent Job exists), the low one stays
// Pending.
func TestRemediationPriorityOrder(t *testing.T) {
	cl := startCluster(t)
	gh := fakegithub.New()
	t.Cleanup(gh.Close)
	cl.githubCredentials(t, gh.URL)
	ctx := context.Background()

	repoURL := "https://127.0.0.1/acme/shop"
	now := metav1.Now()

	mkQueued := func(name string, sev v1alpha1.Level, rating v1alpha1.Rating) *v1alpha1.Finding {
		return fabricateFinding(t, cl, name, sev, repoURL, func(st *v1alpha1.FindingStatus) {
			st.Phase = v1alpha1.PhaseQueued
			st.PhaseTimes = []v1alpha1.PhaseTime{{Phase: v1alpha1.PhaseQueued, At: now}}
			st.Attempts = v1alpha1.AttemptCounts{Investigation: 1, Remediation: 1}
			st.Investigation = &v1alpha1.InvestigationSummary{
				Name: name + "-inv-1", Attempt: 1, Outcome: "ok",
				Recommendation: v1alpha1.RecommendationRemediate,
				Confidence:     "0.9",
				Exploitability: rating, Likelihood: rating, Impact: rating,
				CompletedAt: &now,
			}
		})
	}
	low := mkQueued("finding-aaaaaaaaaa-1", v1alpha1.LevelLow, v1alpha1.RatingLow)
	high := mkQueued("finding-bbbbbbbbbb-1", v1alpha1.LevelCritical, v1alpha1.RatingCritical)

	for _, fnd := range []*v1alpha1.Finding{low, high} {
		// The pinned source tree each remediation would run against.
		if err := cl.client.Create(ctx, &v1alpha1.Repository{
			ObjectMeta: metav1.ObjectMeta{Name: fnd.Name + "-src", Namespace: namespace},
			Spec:       v1alpha1.RepositorySpec{URL: repoURL},
		}); err != nil {
			t.Fatal(err)
		}
		// The completed analysis the remediation executes.
		inv := &v1alpha1.Investigation{
			ObjectMeta: metav1.ObjectMeta{Name: fnd.Name + "-inv-1", Namespace: namespace},
			Spec: v1alpha1.InvestigationSpec{
				FindingRef:    v1alpha1.ObjectReference{Name: fnd.Name, UID: fnd.UID},
				Attempt:       1,
				RepositoryRef: &v1alpha1.LocalObjectReference{Name: fnd.Name + "-src"},
			},
		}
		if err := cl.client.Create(ctx, inv); err != nil {
			t.Fatal(err)
		}
		inv.Status.Phase = v1alpha1.RunComplete
		inv.Status.Report = "---\nanalysis---\nfix the sink\n"
		if err := cl.client.Status().Update(ctx, inv); err != nil {
			t.Fatal(err)
		}
	}

	// Both Remediations exist, Pending, before the scheduler starts — the
	// grant decision then depends on priority alone, not arrival order.
	mkRem := func(fnd *v1alpha1.Finding, prio int32) {
		if err := cl.client.Create(ctx, &v1alpha1.Remediation{
			ObjectMeta: metav1.ObjectMeta{Name: fnd.Name + "-rem-1", Namespace: namespace},
			Spec: v1alpha1.RemediationSpec{
				FindingRef:       v1alpha1.ObjectReference{Name: fnd.Name, UID: fnd.UID},
				InvestigationRef: v1alpha1.ObjectReference{Name: fnd.Name + "-inv-1"},
				RepositoryRef:    v1alpha1.LocalObjectReference{Name: fnd.Name + "-src"},
				Attempt:          1,
				Priority:         prio,
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	mkRem(low, 10)
	mkRem(high, 90)

	cl.controller(t, "source-controller",
		"--artifact-addr", fmt.Sprintf("127.0.0.1:%d", freePort(t)),
		"--artifact-base-url", "http://127.0.0.1:1",
		"--artifact-dir", t.TempDir())
	// Repositories must be Ready before the remediation launch path needs
	// them.
	for _, fnd := range []*v1alpha1.Finding{low, high} {
		eventually(t, "repository artifact for "+fnd.Name, func() bool {
			var repo v1alpha1.Repository
			key := types.NamespacedName{Namespace: namespace, Name: fnd.Name + "-src"}
			return cl.client.Get(ctx, key, &repo) == nil && repo.Status.Artifact != nil
		})
	}

	cl.controller(t, "remediation-controller",
		"--agent-image", "patchy/agent-runner:e2e",
		"--max-concurrent-remediations", "1")

	// The critical finding wins the single slot: its Remediation runs and
	// its Job exists.
	eventually(t, "the high-priority remediation to be granted", func() bool {
		var rem v1alpha1.Remediation
		key := types.NamespacedName{Namespace: namespace, Name: high.Name + "-rem-1"}
		return cl.client.Get(ctx, key, &rem) == nil &&
			rem.Status.Phase == v1alpha1.RunRunning && rem.Status.JobRef != nil
	})
	var jobsList batchv1.JobList
	if err := cl.client.List(ctx, &jobsList, client.InNamespace(agentsNS)); err != nil {
		t.Fatal(err)
	}
	if len(jobsList.Items) != 1 {
		t.Fatalf("agent jobs = %d, want exactly the granted remediation's", len(jobsList.Items))
	}
	if got := jobsList.Items[0].Labels[v1alpha1.LabelFinding]; got != high.Name {
		t.Errorf("running job belongs to %q, want the critical finding %q", got, high.Name)
	}

	// The low-priority one keeps waiting for the slot (the Job above never
	// completes — envtest has no kubelet).
	for range 10 {
		var rem v1alpha1.Remediation
		key := types.NamespacedName{Namespace: namespace, Name: low.Name + "-rem-1"}
		if err := cl.client.Get(ctx, key, &rem); err != nil {
			t.Fatal(err)
		}
		if rem.Status.Phase == v1alpha1.RunRunning {
			t.Fatal("the low-priority remediation was granted past the concurrency cap")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// ---- human signals, rollup, TTL --------------------------------------------

// TestSignalsRollupTTL drives the tail of the lifecycle with fabricated
// findings: a merged remediation PR completes one (edge 16), an /approve
// comment queues a held one (edge 10), the completed finding rolls into the
// all-time statistics, and the TTL deletes it — leaving the rollup behind.
func TestSignalsRollupTTL(t *testing.T) {
	cl := startCluster(t)
	gh := fakegithub.New()
	t.Cleanup(gh.Close)
	cl.githubCredentials(t, gh.URL)
	ctx := context.Background()

	now := metav1.Now()
	// The finding the merged PR completes. Its name matches the fixture's
	// head ref (patchy/finding-cccccccccc-1); the rollup finalizers gate its
	// deletion on the statistics being recorded.
	merged := &v1alpha1.Finding{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "finding-cccccccccc-1",
			Namespace:  namespace,
			Finalizers: []string{v1alpha1.FinalizerRollupTotal, v1alpha1.FinalizerRollupRepository},
		},
		Spec: v1alpha1.FindingSpec{
			IntegrationRef: v1alpha1.LocalObjectReference{Name: "github"},
			Source:         "ghas",
			Advisories:     []string{"CWE-79"},
			Severity:       v1alpha1.LevelHigh,
			Repository: &v1alpha1.FindingRepository{
				Type: v1alpha1.RepositoryTypeGitHub,
				URL:  "https://127.0.0.1/acme/shop", Name: "acme/shop", DefaultBranch: "main",
			},
		},
	}
	if err := cl.client.Create(ctx, merged); err != nil {
		t.Fatal(err)
	}
	merged.Status.Phase = v1alpha1.PhaseInReview
	merged.Status.PhaseTimes = []v1alpha1.PhaseTime{{Phase: v1alpha1.PhaseInReview, At: now}}
	merged.Status.PullRequest = &v1alpha1.PullRequestStatus{Number: 901, State: "open"}
	if err := cl.client.Status().Update(ctx, merged); err != nil {
		t.Fatal(err)
	}

	// The finding a human /approve releases (repository-less: it queues but
	// spawns nothing, which is all this test needs).
	held := fabricateFinding(t, cl, "finding-dddddddddd-1", v1alpha1.LevelHigh, "",
		func(st *v1alpha1.FindingStatus) {
			st.Phase = v1alpha1.PhaseAwaitingApproval
			st.PhaseTimes = []v1alpha1.PhaseTime{{Phase: v1alpha1.PhaseAwaitingApproval, At: now}}
			st.Tracking = &v1alpha1.TrackingStatus{
				Integration: "github", IssueNumber: 102,
				URL: "https://github.com/acme/shop/issues/102", State: "open",
			}
			st.Investigation = &v1alpha1.InvestigationSummary{
				Name: "finding-dddddddddd-1-inv-1", Attempt: 1,
				Recommendation: v1alpha1.RecommendationRemediate, AwaitApproval: true,
			}
		})

	listen := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	cl.controller(t, "integration-controller", "--listen-addr", listen)
	cl.controller(t, "remediation-controller",
		"--agent-image", "patchy/agent-runner:e2e",
		"--finding-ttl", "3s")
	webhookURL := "http://" + listen + "/github/webhooks"

	// The merged PR completes the finding.
	deliver(t, webhookURL, "pull_request", fixture(t, "pull_request.merged.json"))
	eventually(t, "the merged PR to complete the finding", func() bool {
		var cur v1alpha1.Finding
		if err := cl.client.Get(ctx, client.ObjectKeyFromObject(merged), &cur); err != nil {
			return false
		}
		return cur.Status.Phase == v1alpha1.PhaseRemediated && cur.Status.CompletedAt != nil
	})

	// The /approve comment queues the held finding.
	deliver(t, webhookURL, "issue_comment", fixture(t, "issue_comment.approve.json"))
	eventually(t, "the approval to queue the held finding", func() bool {
		var cur v1alpha1.Finding
		if err := cl.client.Get(ctx, client.ObjectKeyFromObject(held), &cur); err != nil {
			return false
		}
		return cur.Spec.Approval != nil && cur.Spec.Approval.By == "octocat" &&
			cur.Status.Phase == v1alpha1.PhaseQueued
	})

	// The completed finding rolls into the all-time statistics.
	eventually(t, "the total rollup to count the finding", func() bool {
		var rollups v1alpha1.FindingRollupList
		if err := cl.client.List(ctx, &rollups, client.InNamespace(namespace)); err != nil {
			return false
		}
		for _, fr := range rollups.Items {
			if fr.Spec.Scope.Type == v1alpha1.ScopeTotal &&
				fr.Status.Bucket.Findings >= 1 &&
				fr.Status.Bucket.Phases["remediated"] >= 1 {
				return true
			}
		}
		return false
	})

	// The TTL deletes the finding; the rollup outlives it.
	eventually(t, "the TTL to delete the completed finding", func() bool {
		var cur v1alpha1.Finding
		err := cl.client.Get(ctx, client.ObjectKeyFromObject(merged), &cur)
		return err != nil
	})
	var rollups v1alpha1.FindingRollupList
	if err := cl.client.List(ctx, &rollups, client.InNamespace(namespace)); err != nil || len(rollups.Items) == 0 {
		t.Fatalf("rollups after TTL = %d (err %v), want the statistics to survive deletion", len(rollups.Items), err)
	}
}

// ---- security --------------------------------------------------------------

// TestForgedSignatureRejected is the check the whole webhook surface rests
// on: an unsigned or wrongly-signed delivery must never reach a handler.
func TestForgedSignatureRejected(t *testing.T) {
	cl := startCluster(t)
	gh := fakegithub.New()
	t.Cleanup(gh.Close)
	cl.githubCredentials(t, gh.URL)
	ctx := context.Background()

	listen := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	cl.controller(t, "integration-controller", "--listen-addr", listen)

	payload := fixture(t, "code_scanning_alert.created.json")
	mac := hmac.New(sha256.New, []byte("the-wrong-secret"))
	mac.Write(payload)

	req, err := http.NewRequest(http.MethodPost, "http://"+listen+"/github/webhooks", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	req.Header.Set("X-GitHub-Event", "code_scanning_alert")
	req.Header.Set("X-GitHub-Delivery", "forged")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("forged delivery: status %d, want 401", resp.StatusCode)
	}

	// Give the receiver a moment, then assert nothing was ingested.
	time.Sleep(500 * time.Millisecond)
	var list v1alpha1.FindingList
	if err := cl.client.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 0 {
		t.Errorf("a forged delivery created %d findings", len(list.Items))
	}
}
