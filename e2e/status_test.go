// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/e2e/fakegithub"
)

// statusDataset mirrors the slice of the status server's payload this test
// asserts (the full contract is unit-tested in internal/web).
type statusDataset struct {
	Namespace string `json:"namespace"`
	Findings  []struct {
		Name        string   `json:"name"`
		Phase       string   `json:"phase"`
		Suspend     bool     `json:"suspend"`
		UserActions []string `json:"userActions"`
	} `json:"findings"`
	Rollups []struct {
		Scope struct {
			Type string `json:"type"`
		} `json:"scope"`
	} `json:"rollups"`
	User *struct {
		Name string `json:"name"`
	} `json:"user"`
}

func getDataset(t *testing.T, url string) (*http.Response, *statusDataset) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	var ds statusDataset
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&ds); err != nil {
			t.Fatalf("decode %s: %v", url, err)
		}
	}
	return resp, &ds
}

// TestStatusServer drives the shipped status-server binary against the
// envtest cluster: the split public/authenticated API, the SSE change
// signal, and — with the real remediation-controller running — the proof
// that a status-page approval is spec-only: the controller, not the server,
// moves AwaitingApproval → Queued.
func TestStatusServer(t *testing.T) {
	cl := startCluster(t)
	gh := fakegithub.New()
	t.Cleanup(gh.Close)
	cl.githubCredentials(t, gh.URL)
	ctx := context.Background()

	now := metav1.Now()
	awaiting := fabricateFinding(t, cl, "finding-approve-1", v1alpha1.LevelHigh,
		"https://127.0.0.1/acme/shop", func(st *v1alpha1.FindingStatus) {
			st.Phase = v1alpha1.PhaseAwaitingApproval
			st.PhaseTimes = []v1alpha1.PhaseTime{{Phase: v1alpha1.PhaseAwaitingApproval, At: now}}
			st.Investigation = &v1alpha1.InvestigationSummary{
				Name: "finding-approve-1-inv-1", Attempt: 1, Outcome: "ok",
				Recommendation: v1alpha1.RecommendationRemediate,
				Confidence:     "0.6", AwaitApproval: true, CompletedAt: &now,
			}
		})
	suspendable := fabricateFinding(t, cl, "finding-suspend-1", v1alpha1.LevelLow,
		"https://127.0.0.1/acme/shop", func(st *v1alpha1.FindingStatus) {
			st.Phase = v1alpha1.PhaseOpened
			st.PhaseTimes = []v1alpha1.PhaseTime{{Phase: v1alpha1.PhaseOpened, At: now}}
		})
	rollup := &v1alpha1.FindingRollup{
		ObjectMeta: metav1.ObjectMeta{Name: "total", Namespace: namespace},
		Spec:       v1alpha1.FindingRollupSpec{Scope: v1alpha1.RollupScope{Type: v1alpha1.ScopeTotal}},
	}
	if err := cl.client.Create(ctx, rollup); err != nil {
		t.Fatal(err)
	}
	rollup.Status = v1alpha1.FindingRollupStatus{
		SchemaVersion: 1,
		Bucket:        v1alpha1.RollupBucket{Findings: 3, Attempts: 4},
	}
	if err := cl.client.Status().Update(ctx, rollup); err != nil {
		t.Fatal(err)
	}

	// The spawner that reacts to the approval.
	cl.controller(t, "remediation-controller",
		"--agent-image", "patchy/agent-runner:e2e",
		"--max-concurrent-remediations", "1")

	// Instance A: auth mode none — full access, no sign-in.
	authCfg := filepath.Join(t.TempDir(), "auth.yaml")
	if err := os.WriteFile(authCfg, []byte("mode: none\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	listen := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	cl.controller(t, "status-server", "--listen-addr", listen, "--auth-config", authCfg)
	base := "http://" + listen

	resp, ds := getDataset(t, base+"/api/findings")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/findings: %d", resp.StatusCode)
	}
	if ds.Namespace != namespace || len(ds.Findings) != 2 || len(ds.Rollups) != 1 {
		t.Fatalf("dataset: ns=%q findings=%d rollups=%d", ds.Namespace, len(ds.Findings), len(ds.Rollups))
	}
	for _, f := range ds.Findings {
		for _, verb := range []string{"approve", "suspend", "resume"} {
			if !slices.Contains(f.UserActions, verb) {
				t.Errorf("finding %s userActions = %v, want all verbs (mode none)", f.Name, f.UserActions)
			}
		}
	}

	// The public statistics surface: rollups only.
	resp, ds = getDataset(t, base+"/api/rollups")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/rollups: %d", resp.StatusCode)
	}
	if len(ds.Findings) != 0 || len(ds.Rollups) != 1 || ds.Rollups[0].Scope.Type != "total" {
		t.Fatalf("rollups dataset: findings=%d rollups=%+v", len(ds.Findings), ds.Rollups)
	}

	// SSE: a change publishes findings-changed.
	events := make(chan string, 8)
	sseResp, err := http.Get(base + "/events")
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	t.Cleanup(func() { sseResp.Body.Close() })
	go func() {
		scanner := bufio.NewScanner(sseResp.Body)
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "event: ") {
				events <- strings.TrimPrefix(scanner.Text(), "event: ")
			}
		}
	}()

	// Approve via the status page: the server records spec.approval; the
	// remediation-controller drives the phase.
	post := func(name, verb string) *http.Response {
		t.Helper()
		resp, err := http.Post(fmt.Sprintf("%s/api/findings/%s/actions/%s", base, name, verb), "", nil)
		if err != nil {
			t.Fatalf("POST %s %s: %v", name, verb, err)
		}
		defer resp.Body.Close()
		return resp
	}
	if resp := post(awaiting.Name, "approve"); resp.StatusCode != http.StatusOK {
		t.Fatalf("approve: %d", resp.StatusCode)
	}
	eventually(t, "the approved finding to be admitted by remediation-controller", func() bool {
		var f v1alpha1.Finding
		key := types.NamespacedName{Namespace: namespace, Name: awaiting.Name}
		if cl.client.Get(ctx, key, &f) != nil || f.Spec.Approval == nil {
			return false
		}
		// The spawner admits to Queued and may immediately grant the slot
		// (Remediating) — either proves the server wrote spec only and the
		// controller moved the phase.
		return f.Status.Phase == v1alpha1.PhaseQueued || f.Status.Phase == v1alpha1.PhaseRemediating
	})

	if resp := post(suspendable.Name, "suspend"); resp.StatusCode != http.StatusOK {
		t.Fatalf("suspend: %d", resp.StatusCode)
	}
	var f v1alpha1.Finding
	if err := cl.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: suspendable.Name}, &f); err != nil {
		t.Fatal(err)
	}
	if !f.Spec.Suspend {
		t.Error("suspend action did not set spec.suspend")
	}

	select {
	case ev := <-events:
		if ev != "findings-changed" {
			t.Errorf("SSE event = %q, want findings-changed", ev)
		}
	case <-time.After(10 * time.Second):
		t.Error("no SSE event after changes")
	}

	// Instance B: no auth config at all — the rollups-only posture.
	publicListen := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	cl.controller(t, "status-server", "--listen-addr", publicListen)
	publicBase := "http://" + publicListen

	if resp, _ := getDataset(t, publicBase+"/api/findings"); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unconfigured GET /api/findings = %d, want 401", resp.StatusCode)
	}
	if resp, ds := getDataset(t, publicBase+"/api/rollups"); resp.StatusCode != http.StatusOK ||
		len(ds.Findings) != 0 || len(ds.Rollups) != 1 {
		t.Errorf("unconfigured GET /api/rollups = %d findings=%d rollups=%d, want public rollups",
			resp.StatusCode, len(ds.Findings), len(ds.Rollups))
	}
}
