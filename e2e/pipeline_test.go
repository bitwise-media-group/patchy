// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package e2e drives the real patchy binaries end to end against a fake
// GitHub API: recorded webhook deliveries in, issues and state transitions
// out. It is the check that the pieces fit together as shipped — no test
// doubles inside the binaries, only at the network boundary.
package e2e

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bitwise-media-group/patchy/e2e/fakegithub"
)

const secret = "e2e-webhook-secret"

// build compiles a controller binary from the product module. e2e is its own
// module, so the build must run in the parent module's directory.
func build(t *testing.T, name string) string {
	t.Helper()
	bin, err := filepath.Abs(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/"+name)
	cmd.Dir = ".."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", name, err, out)
	}
	return bin
}

// freePort reserves an ephemeral port for a controller to listen on.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// controller starts a real controller binary against the fake GitHub API and
// returns its webhook URL.
func controller(t *testing.T, name, secretFile, githubURL string, extra ...string) string {
	t.Helper()
	port := freePort(t)
	args := append([]string{
		"serve",
		"--github-token", "e2e-token",
		"--github-base-url", githubURL,
		"--webhook-secret-file", secretFile,
		"--listen-addr", fmt.Sprintf("127.0.0.1:%d", port),
		"--reconcile-interval", "500ms",
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

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitReady(t, base+"/readyz")
	return base + "/webhook"
}

func waitReady(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
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

// deliver signs and posts a recorded fixture, exactly as GitHub would.
func deliver(t *testing.T, url, event, fixture string) {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join("fixtures", "webhooks", fixture))
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-GitHub-Delivery", fmt.Sprintf("e2e-%s-%d", fixture, time.Now().UnixNano()))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("deliver %s: %v", fixture, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("deliver %s: status %d, want 202", fixture, resp.StatusCode)
	}
}

// eventually polls until cond holds, failing with why after the deadline.
func eventually(t *testing.T, why string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", why)
}

func secretFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "webhook.secret")
	if err := os.WriteFile(path, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestPipeline drives the shipped source- and context-controller binaries
// through the accumulate → age-out → enhance flow using recorded GHAS
// webhook deliveries, asserting the issue state machine advances exactly as
// DESIGN specifies. (The remediation controller needs a Kubernetes cluster,
// so it is covered by its own unit suite and the kind smoke test.)
func TestPipeline(t *testing.T) {
	gh := fakegithub.New()
	t.Cleanup(gh.Close)
	sf := secretFile(t)

	contextFile := filepath.Join(t.TempDir(), "context.yaml")
	if err := os.WriteFile(contextFile, []byte(
		"repos:\n    acme/shop:\n        owners: [octocat]\n        attributes:\n            system: storefront\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}

	source := controller(t, "source-controller", sf, gh.URL, "--accumulation-window", "1h")
	// The context-controller is driven by its reconcile loop here rather
	// than by a webhook, so its URL is never used — starting it is the point.
	_ = controller(t, "context-controller", sf, gh.URL,
		"--static-context-file", contextFile, "--enhance-grace", "100ms")

	// 1. The first CodeQL alert opens a finding issue.
	deliver(t, source, "code_scanning_alert", "code_scanning_alert.created.json")
	eventually(t, "the finding issue to be opened", func() bool {
		return len(gh.Issues()) == 1
	})
	issue := gh.Issues()[0]
	if want := "[ghas] CWE-79: Reflected cross-site scripting"; issue.Title != want {
		t.Errorf("issue title = %q, want %q", issue.Title, want)
	}
	for _, want := range []string{
		"security-source: ghas", "security-advisory: CWE-79", "security-alert: 7",
		"security-finding: opened", "security-accumulation: open",
	} {
		if !slices.Contains(gh.LabelsOf(issue.Number), want) {
			t.Errorf("labels %v missing %q", gh.LabelsOf(issue.Number), want)
		}
	}

	// 2. A second alert of the same finding type accumulates into it —
	//    still one issue, now tracking both alerts.
	deliver(t, source, "code_scanning_alert", "code_scanning_alert.second.json")
	eventually(t, "the second alert to accumulate", func() bool {
		return slices.Contains(gh.LabelsOf(issue.Number), "security-alert: 9")
	})
	if got := len(gh.Issues()); got != 1 {
		t.Fatalf("issues = %d, want 1 (the second alert must accumulate, not open a new issue)", got)
	}

	// 3. The context-controller enhances the issue and advances its state.
	eventually(t, "the issue to be context-enhanced", func() bool {
		return slices.Contains(gh.LabelsOf(issue.Number), "security-finding: context-enhanced")
	})
	if slices.Contains(gh.LabelsOf(issue.Number), "security-finding: opened") {
		t.Error("the opened label survived the enhancement transition")
	}
	comments := strings.Join(gh.Comments(issue.Number), "\n")
	if !strings.Contains(comments, "@octocat") || !strings.Contains(comments, "storefront") {
		t.Errorf("enrichment comment missing the static context:\n%s", comments)
	}
	if !strings.Contains(comments, "patchy:enrichment") {
		t.Error("enrichment comment carries no machine-readable block for the remediation controller")
	}

	// 4. Once the accumulation window elapses, the issue is released to the
	//    remediation pipeline. Ageing the issue past the window is what a
	//    wall-clock hour would do.
	gh.Age(2 * time.Hour)
	eventually(t, "accumulation to complete", func() bool {
		return slices.Contains(gh.LabelsOf(issue.Number), "security-accumulation: complete")
	})
	if slices.Contains(gh.LabelsOf(issue.Number), "security-accumulation: open") {
		t.Error("the issue is still accumulating after the window elapsed")
	}

	// The issue is now exactly what the remediation controller picks up:
	// context-enhanced, accumulation-complete, older than the window.
	got := gh.LabelsOf(issue.Number)
	for _, want := range []string{"security-finding: context-enhanced", "security-accumulation: complete"} {
		if !slices.Contains(got, want) {
			t.Errorf("final labels %v missing %q", got, want)
		}
	}
}

// TestWebhookControllerRoutes drives a delivery through the
// webhook-controller — the single entry point GitHub's one webhook URL
// points at in production — and asserts event-type routing delivers it to
// the controller that consumes it: the source-controller opens the finding
// issue (and the context-controller, which the route does NOT name, still
// enhances it via its reconcile loop). Nothing posts to the controllers
// directly; if the routing breaks, this test does.
func TestWebhookControllerRoutes(t *testing.T) {
	gh := fakegithub.New()
	t.Cleanup(gh.Close)
	sf := secretFile(t)

	contextFile := filepath.Join(t.TempDir(), "context.yaml")
	if err := os.WriteFile(contextFile, []byte(
		"repos:\n    acme/shop:\n        owners: [octocat]\n        attributes:\n            system: storefront\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}

	source := controller(t, "source-controller", sf, gh.URL, "--accumulation-window", "1h")
	enhancer := controller(t, "context-controller", sf, gh.URL,
		"--static-context-file", contextFile, "--enhance-grace", "100ms")
	entry := controller(t, "webhook-controller", sf, gh.URL,
		"--forward-routes", "code_scanning_alert="+source+",issues="+enhancer)

	deliver(t, entry, "code_scanning_alert", "code_scanning_alert.created.json")

	eventually(t, "the finding issue to be opened via the webhook-controller", func() bool {
		return len(gh.Issues()) == 1
	})
	issue := gh.Issues()[0]
	eventually(t, "the issue to be context-enhanced", func() bool {
		return slices.Contains(gh.LabelsOf(issue.Number), "security-finding: context-enhanced")
	})
}

// TestReplayRejectsForgedSignature is the security check the whole webhook
// surface rests on: an unsigned or wrongly-signed delivery must never reach a
// handler.
func TestReplayRejectsForgedSignature(t *testing.T) {
	gh := fakegithub.New()
	t.Cleanup(gh.Close)
	url := controller(t, "source-controller", secretFile(t), gh.URL)

	payload, err := os.ReadFile(filepath.Join("fixtures", "webhooks", "code_scanning_alert.created.json"))
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, []byte("the-wrong-secret"))
	mac.Write(payload)

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
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
	if got := len(gh.Issues()); got != 0 {
		t.Errorf("a forged delivery created %d issues", got)
	}
}
