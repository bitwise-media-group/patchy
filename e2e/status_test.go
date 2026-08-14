// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package e2e

import (
	"bufio"
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
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/e2e/fakegithub"
	"github.com/bitwise-media-group/patchy/internal/transcript"
	"github.com/bitwise-media-group/patchy/internal/transcriptstore"
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

// statusFindingDetail mirrors the slice of the per-finding detail payload
// this test asserts.
type statusFindingDetail struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	PhaseTimes  []struct {
		Phase string `json:"phase"`
	} `json:"phaseTimes"`
	Investigation *struct {
		Transcript *struct {
			Turns int `json:"turns"`
		} `json:"transcript"`
	} `json:"investigation"`
	UserActions []string `json:"userActions"`
}

func getFindingDetail(t *testing.T, url string) *statusFindingDetail {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %d", url, resp.StatusCode)
	}
	var d statusFindingDetail
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		t.Fatalf("decode %s: %v", url, err)
	}
	return &d
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
	fabricateTranscript(t, cl, awaiting.Name)
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
		append([]string{"--max-concurrent-remediations", "1"}, runnerArgs...)...)

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

	// The list payload is trimmed: detail-only fields (both fixtures carry a
	// description and a phase log) stay off it.
	listResp, err := http.Get(base + "/api/findings")
	if err != nil {
		t.Fatalf("GET /api/findings (raw): %v", err)
	}
	var raw struct {
		Findings []map[string]any `json:"findings"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&raw); err != nil {
		t.Fatalf("decode raw list: %v", err)
	}
	_ = listResp.Body.Close()
	for _, f := range raw.Findings {
		for _, key := range []string{"description", "phaseTimes", "alerts", "enrichments"} {
			if _, ok := f[key]; ok {
				t.Errorf("list finding %v carried detail field %q", f["name"], key)
			}
		}
	}

	// The per-finding detail carries what the list omits, including the run
	// detail lifted from the Investigation child.
	detail := getFindingDetail(t, base+"/api/findings/"+awaiting.Name)
	if detail.Description == "" || len(detail.PhaseTimes) == 0 {
		t.Errorf("detail = %+v, want description and phaseTimes", detail)
	}
	if detail.Investigation == nil || detail.Investigation.Transcript == nil ||
		detail.Investigation.Transcript.Turns != 2 {
		t.Errorf("detail investigation = %+v, want transcript with 2 turns", detail.Investigation)
	}
	if !slices.Contains(detail.UserActions, "approve") {
		t.Errorf("detail userActions = %v", detail.UserActions)
	}

	// An unknown (TTL-expired) finding is 404, not an error page.
	if resp, err := http.Get(base + "/api/findings/no-such-finding"); err != nil {
		t.Fatalf("GET detail (missing): %v", err)
	} else {
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET detail (missing) = %d, want 404", resp.StatusCode)
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

	// The stream interleaves config-changed (the Integration/Forge informers
	// also fire at startup); the finding writes above must surface as a
	// findings-changed among them.
	deadline := time.After(10 * time.Second)
	for waiting := true; waiting; {
		select {
		case ev := <-events:
			if ev == "findings-changed" {
				waiting = false
			}
		case <-deadline:
			t.Error("no findings-changed SSE event after changes")
			waiting = false
		}
	}

	// The agent transcript: a completed run replays its stored conversation.
	transcriptPath := "/api/findings/" + awaiting.Name + "/runs/investigation/1/transcript"
	turns := fetchTranscript(t, base+transcriptPath)
	if len(turns) != 2 {
		t.Fatalf("transcript returned %d turns, want 2: %+v", len(turns), turns)
	}
	if turns[0].Kind != "text" || turns[0].Text != "Tracing the sink." {
		t.Errorf("turns[0] = %+v", turns[0])
	}
	if turns[1].Tool != "Read" || turns[1].Text != "app.js" {
		t.Errorf("turns[1] = %+v", turns[1])
	}

	// Instance B: no auth config at all — the rollups-only posture.
	publicListen := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	cl.controller(t, "status-server", "--listen-addr", publicListen)
	publicBase := "http://" + publicListen

	if resp, _ := getDataset(t, publicBase+"/api/findings"); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unconfigured GET /api/findings = %d, want 401", resp.StatusCode)
	}
	// The detail route shares the findings gate exactly.
	if resp, err := http.Get(publicBase + "/api/findings/" + awaiting.Name); err != nil {
		t.Fatalf("GET detail (unconfigured): %v", err)
	} else {
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("unconfigured GET detail = %d, want 401", resp.StatusCode)
		}
	}
	if resp, ds := getDataset(t, publicBase+"/api/rollups"); resp.StatusCode != http.StatusOK ||
		len(ds.Findings) != 0 || len(ds.Rollups) != 1 {
		t.Errorf("unconfigured GET /api/rollups = %d findings=%d rollups=%d, want public rollups",
			resp.StatusCode, len(ds.Findings), len(ds.Rollups))
	}
	// A transcript is finding data, so the rollups-only posture must refuse it
	// exactly as it refuses the findings projection.
	resp, err = http.Get(publicBase + transcriptPath)
	if err != nil {
		t.Fatalf("GET transcript (unconfigured): %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unconfigured GET transcript = %d, want 401", resp.StatusCode)
	}
}

// fabricateTranscript writes a completed Investigation with a stored
// conversation, as the investigation controller would at Job completion.
func fabricateTranscript(t *testing.T, cl *cluster, finding string) {
	t.Helper()
	ctx := context.Background()
	inv := &v1alpha1.Investigation{
		ObjectMeta: metav1.ObjectMeta{
			Name: finding + "-inv-1", Namespace: namespace,
			// The finding label is how the detail projection finds child runs;
			// the real controllers stamp it on every child.
			Labels: map[string]string{v1alpha1.LabelFinding: finding},
		},
		Spec: v1alpha1.InvestigationSpec{
			FindingRef: v1alpha1.ObjectReference{Name: finding}, Attempt: 1,
		},
	}
	if err := cl.client.Create(ctx, inv); err != nil {
		t.Fatal(err)
	}
	turns := []transcript.Turn{
		{Seq: 1, Role: transcript.RoleAssistant, Kind: transcript.KindText, Text: "Tracing the sink."},
		{Seq: 2, Role: transcript.RoleAssistant, Kind: transcript.KindToolUse, Tool: "Read", Text: "app.js"},
	}
	cm, err := transcriptstore.ConfigMap(namespace, nil, inv,
		metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "Investigation"}, turns)
	if err != nil {
		t.Fatal(err)
	}
	if err := cl.client.Create(ctx, cm); err != nil {
		t.Fatal(err)
	}

	inv.Status = v1alpha1.InvestigationStatus{
		Phase: v1alpha1.RunComplete,
		Stage: &v1alpha1.StageResult{
			Outcome:    "ok",
			Transcript: &v1alpha1.TranscriptRef{Name: cm.Name, Turns: int32(len(turns))},
		},
	}
	if err := cl.client.Status().Update(ctx, inv); err != nil {
		t.Fatal(err)
	}
}

// fetchTranscript reads the turn events off the transcript SSE stream.
func fetchTranscript(t *testing.T, url string) []statusTurn {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}

	var turns []statusTurn
	ended := false
	for _, block := range strings.Split(string(body), "\n\n") {
		var event, data string
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data = strings.TrimPrefix(line, "data: ")
			}
		}
		switch event {
		case "turn":
			var turn statusTurn
			if err := json.Unmarshal([]byte(data), &turn); err != nil {
				t.Fatalf("decode turn %q: %v", data, err)
			}
			turns = append(turns, turn)
		case "end":
			ended = true
		}
	}
	if !ended {
		t.Error("transcript stream did not end")
	}
	return turns
}

// statusTurn mirrors the status server's transcript wire type.
type statusTurn struct {
	Seq  int    `json:"seq"`
	Role string `json:"role"`
	Kind string `json:"kind"`
	Tool string `json:"tool"`
	Text string `json:"text"`
}
