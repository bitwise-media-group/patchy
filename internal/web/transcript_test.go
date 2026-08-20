// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/kube"
	"github.com/bitwise-media-group/patchy/internal/transcript"
	"github.com/bitwise-media-group/patchy/internal/transcriptstore"
)

const transcriptPath = "/api/findings/finding-aa-1/runs/investigation/1/transcript"

func storedTurns() []transcript.Turn {
	return []transcript.Turn{
		{Seq: 1, Role: transcript.RoleSystem, Kind: transcript.KindNotice, Text: "session s1 started"},
		{Seq: 2, Role: transcript.RoleAssistant, Kind: transcript.KindText, Text: "Reading the sink."},
		{Seq: 3, Role: transcript.RoleAssistant, Kind: transcript.KindToolUse, Tool: "Read", Text: "app.js"},
	}
}

// completedInvestigation is a collected run with a persisted transcript.
func completedInvestigation(t *testing.T) []client.Object {
	t.Helper()
	raw, err := transcriptstore.Marshal(storedTurns())
	if err != nil {
		t.Fatal(err)
	}
	return []client.Object{
		&v1alpha1.Investigation{
			ObjectMeta: metav1.ObjectMeta{Name: "finding-aa-1-inv-1", Namespace: "patchy"},
			Spec: v1alpha1.InvestigationSpec{
				FindingRef: v1alpha1.ObjectReference{Name: "finding-aa-1"}, Attempt: 1,
			},
			Status: v1alpha1.InvestigationStatus{
				Phase: v1alpha1.RunComplete,
				Stage: &v1alpha1.StageResult{
					Outcome:    "ok",
					Transcript: &v1alpha1.TranscriptRef{Name: "finding-aa-1-inv-1-transcript", Turns: 3},
				},
			},
		},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "finding-aa-1-inv-1-transcript", Namespace: "patchy"},
			BinaryData: map[string][]byte{transcriptstore.DataKey: raw},
		},
	}
}

// runningInvestigation has a live Job and no persisted transcript yet.
func runningInvestigation() []client.Object {
	return []client.Object{
		&v1alpha1.Investigation{
			ObjectMeta: metav1.ObjectMeta{Name: "finding-aa-1-inv-1", Namespace: "patchy"},
			Spec: v1alpha1.InvestigationSpec{
				FindingRef: v1alpha1.ObjectReference{Name: "finding-aa-1"}, Attempt: 1,
			},
			Status: v1alpha1.InvestigationStatus{
				Phase:  v1alpha1.RunRunning,
				JobRef: &v1alpha1.JobReference{Namespace: "patchy-agents", Name: "job-1"},
			},
		},
	}
}

// fakeTailer replays canned turns, optionally blocking until released.
type fakeTailer struct {
	turns   []transcript.Turn
	hold    chan struct{}
	started chan struct{}
}

func (f *fakeTailer) Tail(ctx context.Context, _ string, fn func(transcript.Turn) error) error {
	if f.started != nil {
		close(f.started)
	}
	for _, t := range f.turns {
		if err := fn(t); err != nil {
			return err
		}
	}
	if f.hold != nil {
		select {
		case <-f.hold:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func transcriptServer(t *testing.T, tailer Tailer, objs ...client.Object) *Server {
	t.Helper()
	c := fake.NewClientBuilder().
		WithScheme(kube.Scheme()).
		WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.Finding{}, &v1alpha1.Investigation{}, &v1alpha1.Remediation{}).
		Build()
	s := NewServer(c, "patchy", stubAuth{id: operator}, stubGranter{grants: allGrants()}, nil)
	return s.WithTranscripts(c, tailer)
}

// sseTurns collects the turn events from an SSE body.
func sseTurns(t *testing.T, body string) ([]Turn, bool) {
	t.Helper()
	var turns []Turn
	ended := false
	for block := range strings.SplitSeq(body, "\n\n") {
		var event, data string
		for line := range strings.SplitSeq(block, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data = strings.TrimPrefix(line, "data: ")
			}
		}
		switch event {
		case eventTurn:
			var turn Turn
			if err := json.Unmarshal([]byte(data), &turn); err != nil {
				t.Fatalf("decode turn %q: %v", data, err)
			}
			turns = append(turns, turn)
		case eventEnd:
			ended = true
		}
	}
	return turns, ended
}

func TestTranscriptRequiresSessionAndViewGrant(t *testing.T) {
	// A transcript is finding data: gated exactly like /api/findings, and
	// never like the deliberately public /events signal.
	cases := []struct {
		name       string
		auth       stubAuth
		granter    stubGranter
		wantStatus int
	}{
		{"no session", stubAuth{}, stubGranter{grants: allGrants()}, http.StatusUnauthorized},
		{"no view grant", stubAuth{id: operator}, stubGranter{}, http.StatusForbidden},
		{"authorised", stubAuth{id: operator}, stubGranter{grants: allGrants()}, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := transcriptServer(t, nil, completedInvestigation(t)...)
			s.auth, s.granter = tc.auth, tc.granter
			ts := httptest.NewServer(s.Handler())
			defer ts.Close()

			res, err := http.Get(ts.URL + transcriptPath)
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			defer func() { _ = res.Body.Close() }()
			if res.StatusCode != tc.wantStatus {
				t.Errorf("status = %d, want %d", res.StatusCode, tc.wantStatus)
			}
		})
	}
}

// An attempt outside int32 must be rejected, not folded into a real one:
// parsed at 64 bits, 4294967297 clears the `< 1` guard and then truncates to
// attempt 1, quietly serving a different run's conversation than the one asked
// for (CodeQL go/incorrect-integer-conversion, finding 5).
func TestTranscriptRejectsOutOfRangeAttempt(t *testing.T) {
	cases := []struct {
		name       string
		attempt    string
		wantStatus int
	}{
		{"in range", "1", http.StatusOK},
		{"zero", "0", http.StatusBadRequest},
		{"negative", "-1", http.StatusBadRequest},
		{"not a number", "one", http.StatusBadRequest},
		{"wraps to 1", "4294967297", http.StatusBadRequest},
		{"wraps to 0", "4294967296", http.StatusBadRequest},
		{"wraps negative", "2147483648", http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := transcriptServer(t, nil, completedInvestigation(t)...)
			ts := httptest.NewServer(s.Handler())
			defer ts.Close()

			res, err := http.Get(ts.URL + "/api/findings/finding-aa-1/runs/investigation/" + tc.attempt + "/transcript")
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			defer func() { _ = res.Body.Close() }()
			if res.StatusCode != tc.wantStatus {
				t.Errorf("status = %d, want %d", res.StatusCode, tc.wantStatus)
			}
		})
	}
}

func TestTranscriptServesPersistedTurns(t *testing.T) {
	s := transcriptServer(t, nil, completedInvestigation(t)...)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	res, err := http.Get(ts.URL + transcriptPath)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	body, _ := io.ReadAll(res.Body)

	turns, ended := sseTurns(t, string(body))
	if len(turns) != 3 {
		t.Fatalf("got %d turns, want 3: %+v", len(turns), turns)
	}
	if turns[2].Tool != "Read" || turns[2].Text != "app.js" {
		t.Errorf("turns[2] = %+v", turns[2])
	}
	if !ended {
		t.Error("stream did not end; a client cannot tell it from a dropped connection")
	}
}

func TestTranscriptStreamsLiveRun(t *testing.T) {
	tailer := &fakeTailer{turns: storedTurns()}
	s := transcriptServer(t, tailer, runningInvestigation()...)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	res, err := http.Get(ts.URL + transcriptPath)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(res.Body)

	turns, ended := sseTurns(t, string(body))
	if len(turns) != 3 {
		t.Fatalf("got %d live turns, want 3: %+v", len(turns), turns)
	}
	if !ended {
		t.Error("live stream did not end when the run finished")
	}
}

func TestTranscriptPrefersPersistedOverLive(t *testing.T) {
	// Once collected, the stored record is complete while the pod log is
	// already being reaped.
	tailer := &fakeTailer{turns: []transcript.Turn{
		{Seq: 9, Role: transcript.RoleAssistant, Kind: transcript.KindText, Text: "from the log"},
	}}
	s := transcriptServer(t, tailer, completedInvestigation(t)...)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	res, err := http.Get(ts.URL + transcriptPath)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(res.Body)

	turns, _ := sseTurns(t, string(body))
	if len(turns) != 3 || turns[0].Text != "session s1 started" {
		t.Errorf("served %+v, want the persisted transcript", turns)
	}
}

func TestTranscriptEmptyWhenNothingRecorded(t *testing.T) {
	inv := &v1alpha1.Investigation{
		ObjectMeta: metav1.ObjectMeta{Name: "finding-aa-1-inv-1", Namespace: "patchy"},
		Spec: v1alpha1.InvestigationSpec{
			FindingRef: v1alpha1.ObjectReference{Name: "finding-aa-1"}, Attempt: 1,
		},
		Status: v1alpha1.InvestigationStatus{Phase: v1alpha1.RunFailed},
	}
	s := transcriptServer(t, nil, inv)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	res, err := http.Get(ts.URL + transcriptPath)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	turns, ended := sseTurns(t, string(body))
	if len(turns) != 0 || !ended {
		t.Errorf("turns = %+v ended = %v, want an empty ended stream", turns, ended)
	}
}

func TestTranscriptRejectsCrossFindingAccess(t *testing.T) {
	// The run name is derivable, so the path's finding must be checked against
	// the run's own findingRef — otherwise naming another finding's attempt
	// would read its conversation.
	objs := completedInvestigation(t)
	inv := objs[0].(*v1alpha1.Investigation)
	inv.Spec.FindingRef.Name = "someone-elses-finding"

	s := transcriptServer(t, nil, objs...)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	res, err := http.Get(ts.URL + transcriptPath)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for a mismatched finding", res.StatusCode)
	}
}

func TestTranscriptBadRequests(t *testing.T) {
	s := transcriptServer(t, nil, completedInvestigation(t)...)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	cases := []struct {
		name, path string
		want       int
	}{
		{"unknown kind", "/api/findings/finding-aa-1/runs/audit/1/transcript", http.StatusNotFound},
		{"missing run", "/api/findings/finding-aa-1/runs/investigation/9/transcript", http.StatusNotFound},
		{"attempt zero", "/api/findings/finding-aa-1/runs/investigation/0/transcript", http.StatusBadRequest},
		{"attempt not a number", "/api/findings/finding-aa-1/runs/investigation/x/transcript", http.StatusBadRequest},
	}
	for _, tc := range cases {
		res, err := http.Get(ts.URL + tc.path)
		if err != nil {
			t.Fatalf("%s: GET: %v", tc.name, err)
		}
		_ = res.Body.Close()
		if res.StatusCode != tc.want {
			t.Errorf("%s: status = %d, want %d", tc.name, res.StatusCode, tc.want)
		}
	}
}

func TestEventsStaysPublicAndContentFree(t *testing.T) {
	// Guarding the invariant the transcript endpoint deliberately does not
	// share: /events carries no finding data, which is why it needs no auth.
	s := transcriptServer(t, nil, completedInvestigation(t)...)
	s.auth = stubAuth{} // no session at all
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/events", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	res, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 (the signal is public)", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	if strings.Contains(string(body), "Reading the sink") {
		t.Error("/events leaked transcript content")
	}
}
