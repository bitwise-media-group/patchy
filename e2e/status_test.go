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
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
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
	Matched   int    `json:"matched"`
	Facets    *struct {
		Total int `json:"total"`
	} `json:"facets"`
	Findings []struct {
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
	defer func() { _ = resp.Body.Close() }()
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
	defer func() { _ = resp.Body.Close() }()
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
// signal, and (with the real remediation-controller running) the proof
// that a status-page approval is spec-only: the controller, not the server,
// moves AwaitingApproval → Queued.
func TestStatusServer(t *testing.T) {
	cl := startCluster(t)
	gh := fakegithub.New()
	t.Cleanup(gh.Close)
	cl.githubCredentials(t, gh.URL)

	awaiting, suspendable := statusFixtures(t, cl)

	// The spawner that reacts to the approval.
	cl.controller(t, "remediation-controller",
		append([]string{"--max-concurrent-remediations", "1"}, runnerArgs...)...)

	// Instance A: auth mode none (full access, no sign-in).
	authCfg := filepath.Join(t.TempDir(), "auth.yaml")
	if err := os.WriteFile(authCfg, []byte("mode: none\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	listen := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	cl.controller(t, "status-server", "--listen-addr", listen, "--auth-config", authCfg)
	base := "http://" + listen

	statusCheckList(t, base)
	statusCheckDetail(t, base, awaiting.Name)
	statusCheckActions(t, cl, base, awaiting.Name, suspendable.Name)
	transcriptPath := statusCheckTranscripts(t, cl, base, awaiting.Name)
	statusCheckUnconfigured(t, cl, awaiting.Name, transcriptPath)
}

// statusFixtures fabricates the findings, transcript and rollup the status
// server serves: one finding awaiting approval, one to suspend.
func statusFixtures(t *testing.T, cl *cluster) (awaiting, suspendable *v1alpha1.Finding) {
	t.Helper()
	ctx := context.Background()
	now := metav1.Now()
	awaiting = fabricateFinding(t, cl, "finding-approve-1", v1alpha1.LevelHigh,
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
	suspendable = fabricateFinding(t, cl, "finding-suspend-1", v1alpha1.LevelLow,
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
	return awaiting, suspendable
}

// statusCheckList checks the trimmed findings list (mode none: every verb).
func statusCheckList(t *testing.T, base string) {
	t.Helper()

	resp, ds := getDataset(t, base+"/api/findings")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/findings: %d", resp.StatusCode)
	}
	// The findings list carries no rollups; the metrics summary is /api/rollups.
	if ds.Namespace != namespace || len(ds.Findings) != 2 || len(ds.Rollups) != 0 {
		t.Fatalf("dataset: ns=%q findings=%d rollups=%d", ds.Namespace, len(ds.Findings), len(ds.Rollups))
	}
	// The first page carries the facets; both findings fit on it.
	if ds.Matched != 2 || ds.Facets == nil || ds.Facets.Total != 2 {
		t.Fatalf("first page: matched=%d facets=%+v, want 2 and total 2", ds.Matched, ds.Facets)
	}
	statusCheckPaging(t, base)

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
}

// statusCheckPaging walks the two-finding list one row at a time: the
// cursor continues it with the other finding, and only the first page
// carries the facets.
func statusCheckPaging(t *testing.T, base string) {
	t.Helper()
	var first struct {
		Findings   []struct{ Name string } `json:"findings"`
		NextCursor string                  `json:"nextCursor"`
	}
	firstResp, err := http.Get(base + "/api/findings?limit=1")
	if err != nil {
		t.Fatalf("GET limit=1: %v", err)
	}
	if err := json.NewDecoder(firstResp.Body).Decode(&first); err != nil {
		t.Fatalf("decode limit=1 page: %v", err)
	}
	_ = firstResp.Body.Close()
	if len(first.Findings) != 1 || first.NextCursor == "" {
		t.Fatalf("limit=1 page: %+v", first)
	}
	resp, next := getDataset(t, base+"/api/findings?limit=1&cursor="+first.NextCursor)
	if resp.StatusCode != http.StatusOK || len(next.Findings) != 1 || next.Facets != nil ||
		next.Findings[0].Name == first.Findings[0].Name {
		t.Fatalf("cursor page: status %d, %+v", resp.StatusCode, next)
	}
}

// statusCheckDetail checks the per-finding detail, the 404 for an expired
// finding, and the metrics summary.
func statusCheckDetail(t *testing.T, base, awaiting string) {
	t.Helper()
	// The per-finding detail carries what the list omits, including the run
	// detail lifted from the Investigation child.
	detail := getFindingDetail(t, base+"/api/findings/"+awaiting)
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

	// The metrics summary (mode none: authentication disabled, so open).
	resp, ds := getDataset(t, base+"/api/rollups")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/rollups: %d", resp.StatusCode)
	}
	if len(ds.Findings) != 0 || len(ds.Rollups) != 1 || ds.Rollups[0].Scope.Type != "total" {
		t.Fatalf("rollups dataset: findings=%d rollups=%+v", len(ds.Findings), ds.Rollups)
	}
}

// statusCheckActions approves and suspends through the page and checks the
// SSE stream reports the change.
func statusCheckActions(t *testing.T, cl *cluster, base, awaiting, suspendable string) {
	t.Helper()
	ctx := context.Background()
	// SSE: a change publishes findings-changed.
	events := make(chan string, 8)
	sseResp, err := http.Get(base + "/events")
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	t.Cleanup(func() { _ = sseResp.Body.Close() })
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
		defer func() { _ = resp.Body.Close() }()
		return resp
	}
	if resp := post(awaiting, "approve"); resp.StatusCode != http.StatusOK {
		t.Fatalf("approve: %d", resp.StatusCode)
	}
	eventually(t, "the approved finding to be admitted by remediation-controller", func() bool {
		var f v1alpha1.Finding
		key := types.NamespacedName{Namespace: namespace, Name: awaiting}
		if cl.client.Get(ctx, key, &f) != nil || f.Spec.Approval == nil {
			return false
		}
		// The spawner admits to Queued and may immediately grant the slot
		// (Remediating). Either proves the server wrote spec only and the
		// controller moved the phase.
		return f.Status.Phase == v1alpha1.PhaseQueued || f.Status.Phase == v1alpha1.PhaseRemediating
	})

	if resp := post(suspendable, "suspend"); resp.StatusCode != http.StatusOK {
		t.Fatalf("suspend: %d", resp.StatusCode)
	}
	var f v1alpha1.Finding
	if err := cl.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: suspendable}, &f); err != nil {
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
}

// statusCheckTranscripts replays a stored conversation and a compacted long
// one (with resume), returning the short one's transcript path.
func statusCheckTranscripts(t *testing.T, cl *cluster, base, awaiting string) string {
	t.Helper()
	// The agent transcript: a completed run replays its stored conversation.
	transcriptPath := "/api/findings/" + awaiting + "/runs/investigation/1/transcript"
	turns := fetchTranscript(t, base+transcriptPath, "")
	if len(turns) != 2 {
		t.Fatalf("transcript returned %d turns, want 2: %+v", len(turns), turns)
	}
	if turns[0].Kind != "text" || turns[0].Text != "Tracing the sink." {
		t.Errorf("turns[0] = %+v", turns[0])
	}
	if turns[1].Tool != "Read" || turns[1].Text != "app.js" {
		t.Errorf("turns[1] = %+v", turns[1])
	}

	statusCheckLongTranscript(t, cl, base)
	return transcriptPath
}

// statusCheckLongTranscript checks a run that streamed past the store bounds
// replays compacted (head, tail, elision notice) and resumes by seq.
func statusCheckLongTranscript(t *testing.T, cl *cluster, base string) {
	t.Helper()
	ctx := context.Background()
	// A long run: the live stream carried every turn, but the stored record
	// is compacted to fit its ConfigMap (head and tail with an elision
	// notice) and still resumes by seq.
	const longTurns = 1200
	stored := fabricateLongTranscript(t, cl, "finding-long-1", longTurns)
	if stored.Turns > int32(transcript.DefaultStoreMaxTurns) || !stored.Truncated {
		t.Errorf("stored ref = %+v, want at most %d turns and truncated", stored, transcript.DefaultStoreMaxTurns)
	}
	var storedCM corev1.ConfigMap
	if err := cl.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: stored.Name}, &storedCM); err != nil {
		t.Fatalf("get stored transcript: %v", err)
	}
	if n := len(storedCM.BinaryData[transcriptstore.DataKey]); n >= 1<<20 {
		t.Errorf("stored transcript is %d bytes, want under the 1 MiB object cap", n)
	}
	longPath := "/api/findings/finding-long-1/runs/investigation/1/transcript"
	all := fetchTranscript(t, base+longPath, "")
	if len(all) != int(stored.Turns) {
		t.Fatalf("long transcript returned %d turns, stored %d", len(all), stored.Turns)
	}
	if all[0].Seq != 1 || all[len(all)-1].Seq != longTurns {
		t.Errorf("first/last seq = %d/%d, want 1/%d", all[0].Seq, all[len(all)-1].Seq, longTurns)
	}
	elided := false
	for i, turn := range all {
		if i > 0 && turn.Seq <= all[i-1].Seq {
			t.Fatalf("seq not increasing at %d: %d after %d", i, turn.Seq, all[i-1].Seq)
		}
		if len(turn.Text) > transcript.DefaultStoreMaxTurnBytes {
			t.Errorf("turn %d is %d bytes, want <= %d", turn.Seq, len(turn.Text), transcript.DefaultStoreMaxTurnBytes)
		}
		elided = elided || (turn.Kind == "notice" && strings.Contains(turn.Text, "elided"))
	}
	if !elided {
		t.Error("compacted transcript carries no elision notice")
	}
	// A reconnect resumes after the last seen turn instead of replaying.
	resumeAt := all[len(all)-3].Seq
	resumed := fetchTranscript(t, base+longPath, strconv.Itoa(resumeAt))
	if len(resumed) != 2 || resumed[0].Seq != all[len(all)-2].Seq {
		t.Errorf("resume after %d returned %+v, want the last 2 turns", resumeAt, resumed)
	}
}

// statusCheckUnconfigured starts a server with no auth config and checks it
// serves nothing.
func statusCheckUnconfigured(t *testing.T, cl *cluster, awaiting, transcriptPath string) {
	t.Helper()
	// Instance B: no auth config at all, so nothing is served.
	publicListen := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	cl.controller(t, "status-server", "--listen-addr", publicListen)
	publicBase := "http://" + publicListen

	if resp, _ := getDataset(t, publicBase+"/api/findings"); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unconfigured GET /api/findings = %d, want 401", resp.StatusCode)
	}
	// The detail route shares the findings gate exactly.
	if resp, err := http.Get(publicBase + "/api/findings/" + awaiting); err != nil {
		t.Fatalf("GET detail (unconfigured): %v", err)
	} else {
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("unconfigured GET detail = %d, want 401", resp.StatusCode)
		}
	}
	// Metrics and the change stream share the rule: an identity or nothing.
	for _, path := range []string{"/api/rollups", "/api/rollups/repositories", "/events"} {
		resp, err := http.Get(publicBase + path)
		if err != nil {
			t.Fatalf("GET %s (unconfigured): %v", path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("unconfigured GET %s = %d, want 401", path, resp.StatusCode)
		}
	}
	// A transcript is finding data, refused exactly as the findings
	// projection is.
	resp, err := http.Get(publicBase + transcriptPath)
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

// fabricateLongTranscript writes a completed Investigation whose conversation
// ran far past the store bounds (n turns, some larger than a stored turn may
// be) through transcriptstore.Persist, exactly as the investigation
// controller collects a run.
func fabricateLongTranscript(t *testing.T, cl *cluster, finding string, n int) *v1alpha1.TranscriptRef {
	t.Helper()
	ctx := context.Background()
	inv := &v1alpha1.Investigation{
		ObjectMeta: metav1.ObjectMeta{
			Name: finding + "-inv-1", Namespace: namespace,
			Labels: map[string]string{v1alpha1.LabelFinding: finding},
		},
		Spec: v1alpha1.InvestigationSpec{
			FindingRef: v1alpha1.ObjectReference{Name: finding}, Attempt: 1,
		},
	}
	if err := cl.client.Create(ctx, inv); err != nil {
		t.Fatal(err)
	}
	turns := make([]transcript.Turn, n)
	for i := range turns {
		text := fmt.Sprintf("turn %d", i+1)
		if i%10 == 0 {
			text = strings.Repeat(fmt.Sprintf("line %d of a large tool result\n", i), 400) // ~12 KiB
		}
		turns[i] = transcript.Turn{Seq: i + 1, Role: transcript.RoleUser, Kind: transcript.KindToolResult, Text: text}
	}
	ref, err := transcriptstore.Persist(ctx, cl.client, namespace, nil, inv,
		metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "Investigation"}, turns)
	if err != nil {
		t.Fatal(err)
	}
	inv.Status = v1alpha1.InvestigationStatus{
		Phase: v1alpha1.RunComplete,
		Stage: &v1alpha1.StageResult{Outcome: "ok", Transcript: ref},
	}
	if err := cl.client.Status().Update(ctx, inv); err != nil {
		t.Fatal(err)
	}
	return ref
}

// fetchTranscript reads the turn events off the transcript SSE stream,
// resuming after lastEventID when it is set.
func fetchTranscript(t *testing.T, url, lastEventID string) []statusTurn {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	resp, err := http.DefaultClient.Do(req)
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
