// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package evalapi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/kube"
	"github.com/bitwise-media-group/patchy/internal/web/auth"
	"github.com/bitwise-media-group/patchy/pkg/evaluation"
)

const digestA = "aa11bb22cc33dd44ee55ff6600112233445566778899aabbccddeeff00112233"
const digestB = "bb11bb22cc33dd44ee55ff6600112233445566778899aabbccddeeff00112233"

// headerAuth authenticates via a plain test header.
type headerAuth struct{}

func (headerAuth) Identify(r *http.Request) (*auth.Identity, error) {
	switch r.Header.Get("X-Test-User") {
	case "":
		return nil, nil
	case "broken":
		return nil, fmt.Errorf("bad token")
	default:
		return &auth.Identity{Username: r.Header.Get("X-Test-User")}, nil
	}
}

// denyGranter denies one username.
type denyGranter struct{ denied string }

func (g denyGranter) Allowed(_ context.Context, id auth.Identity, _ string) (bool, error) {
	return id.Username != g.denied, nil
}

type fakeWorkspaces struct {
	present map[string]bool
	stored  map[string][]byte
}

func (f *fakeWorkspaces) Stat(_ context.Context, digest string) (bool, error) {
	return f.present[digest], nil
}

func (f *fakeWorkspaces) Put(_ context.Context, digest string, r io.Reader, _ int64) error {
	raw, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if f.stored == nil {
		f.stored = map[string][]byte{}
	}
	f.stored[digest] = raw
	f.present[digest] = true
	return nil
}

func newTestServer(t *testing.T, objs ...client.Object) (*Server, client.Client, *fakeWorkspaces) {
	t.Helper()
	c := fake.NewClientBuilder().
		WithScheme(kube.Scheme()).
		WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.Evaluation{}, &v1alpha1.EvaluationUnit{}).
		Build()
	ws := &fakeWorkspaces{present: map[string]bool{digestA: true}}
	srv := NewServer(c, "patchy", headerAuth{}, evaluation.AuthInfo{Mode: "oidc", Issuer: "https://dex.local"},
		denyGranter{denied: "mallory"}, ws, Limits{}, nil)
	return srv, c, ws
}

func doReq(t *testing.T, srv *Server, method, path, user string, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	if user != "" {
		req.Header.Set("X-Test-User", user)
	}
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	return rr
}

func validSubmission() string {
	sub := evaluation.Submission{
		Version: evaluation.SubmissionVersion,
		Units: []evaluation.UnitSpec{{
			Skill: "workflow-commit", Tier: 2, Model: "anthropic/claude-sonnet-5",
			Harnesses: []evaluation.HarnessOption{{Harness: "claude", ModelID: "claude-sonnet-5"}},
			Workspace: evaluation.WorkspaceRef{Digest: digestA, SizeBytes: 1024},
		}},
	}
	raw, _ := json.Marshal(sub)
	return string(raw)
}

func TestAuthInfoIsPublic(t *testing.T) {
	srv, _, _ := newTestServer(t)
	rr := doReq(t, srv, "GET", "/api/v1/auth/info", "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("auth/info = %d, want 200", rr.Code)
	}
	var info evaluation.AuthInfo
	if err := json.Unmarshal(rr.Body.Bytes(), &info); err != nil || info.Issuer != "https://dex.local" {
		t.Errorf("auth info = %+v (%v)", info, err)
	}
}

func TestAuthMatrix(t *testing.T) {
	srv, _, _ := newTestServer(t)
	tests := []struct {
		name string
		user string
		want int
	}{
		{"anonymous is 401", "", http.StatusUnauthorized},
		{"invalid token is 401", "broken", http.StatusUnauthorized},
		{"denied user is 403", "mallory", http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if rr := doReq(t, srv, "POST", "/api/v1/evaluations", tt.user, validSubmission()); rr.Code != tt.want {
				t.Errorf("POST evaluations = %d, want %d", rr.Code, tt.want)
			}
			if rr := doReq(t, srv, "HEAD", "/api/v1/workspaces/"+digestA, tt.user, ""); rr.Code != tt.want {
				t.Errorf("HEAD workspace = %d, want %d", rr.Code, tt.want)
			}
		})
	}
}

func TestWorkspaceHeadAndPut(t *testing.T) {
	srv, _, ws := newTestServer(t)
	if rr := doReq(t, srv, "HEAD", "/api/v1/workspaces/"+digestA, "dev", ""); rr.Code != http.StatusNoContent {
		t.Errorf("HEAD cached = %d, want 204", rr.Code)
	}
	if rr := doReq(t, srv, "HEAD", "/api/v1/workspaces/"+digestB, "dev", ""); rr.Code != http.StatusNotFound {
		t.Errorf("HEAD absent = %d, want 404", rr.Code)
	}
	if rr := doReq(t, srv, "PUT", "/api/v1/workspaces/"+digestB, "dev", "tarball-bytes"); rr.Code != http.StatusCreated {
		t.Errorf("PUT new = %d, want 201", rr.Code)
	}
	if string(ws.stored[digestB]) != "tarball-bytes" {
		t.Error("uploaded bytes did not reach the workspace client")
	}
	if rr := doReq(t, srv, "PUT", "/api/v1/workspaces/"+digestB, "dev", "tarball-bytes"); rr.Code != http.StatusOK {
		t.Errorf("PUT existing = %d, want 200", rr.Code)
	}
	if rr := doReq(t, srv, "PUT", "/api/v1/workspaces/nothex", "dev", "x"); rr.Code != http.StatusBadRequest {
		t.Errorf("PUT malformed digest = %d, want 400", rr.Code)
	}
}

func TestSubmitCreatesEvaluation(t *testing.T) {
	srv, c, _ := newTestServer(t)
	rr := doReq(t, srv, "POST", "/api/v1/evaluations", "dev@example.com", validSubmission())
	if rr.Code != http.StatusCreated {
		t.Fatalf("POST = %d: %s", rr.Code, rr.Body.String())
	}
	var resp evaluation.SubmissionResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Name == "" || len(resp.Units) != 1 || !strings.HasPrefix(resp.Units[0], resp.Name+"-u") {
		t.Errorf("response = %+v", resp)
	}

	var eval v1alpha1.Evaluation
	if err := c.Get(t.Context(), types.NamespacedName{Namespace: "patchy", Name: resp.Name}, &eval); err != nil {
		t.Fatalf("Get evaluation: %v", err)
	}
	if eval.Spec.Submitter != "dev@example.com" {
		t.Errorf("submitter = %q", eval.Spec.Submitter)
	}
	plan := eval.Spec.Units[0]
	if plan.Skill != "workflow-commit" || plan.Tier != 2 || plan.Workspace.Digest != digestA {
		t.Errorf("plan = %+v", plan)
	}
	var unit evaluation.UnitSpec
	if err := json.Unmarshal([]byte(plan.ExecJSON), &unit); err != nil || unit.Skill != "workflow-commit" {
		t.Errorf("execJSON does not round-trip: %v", err)
	}
}

func TestSubmitValidation(t *testing.T) {
	srv, _, _ := newTestServer(t)
	tests := []struct {
		name string
		body string
		want int
	}{
		{"garbage", "{not json", http.StatusBadRequest},
		{"wrong version", `{"version":"v0","units":[{}]}`, http.StatusBadRequest},
		{"no units", `{"version":"v1","units":[]}`, http.StatusBadRequest},
		{"bad tier", `{"version":"v1","units":[{"skill":"s","tier":3,"model":"m",` +
			`"harnesses":[{"harness":"claude"}],"workspace":{"digest":"` + digestA + `"}}]}`, http.StatusBadRequest},
		{"no harnesses", `{"version":"v1","units":[{"skill":"s","tier":2,"model":"m",` +
			`"workspace":{"digest":"` + digestA + `"}}]}`, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if rr := doReq(t, srv, "POST", "/api/v1/evaluations", "dev", tt.body); rr.Code != tt.want {
				t.Errorf("POST = %d, want %d: %s", rr.Code, tt.want, rr.Body.String())
			}
		})
	}
}

func TestSubmitMissingWorkspaceIs412(t *testing.T) {
	srv, _, _ := newTestServer(t)
	sub := evaluation.Submission{
		Version: evaluation.SubmissionVersion,
		Units: []evaluation.UnitSpec{{
			Skill: "s", Tier: 1, Model: "anthropic/claude-sonnet-5",
			Harnesses: []evaluation.HarnessOption{{Harness: "claude"}},
			Workspace: evaluation.WorkspaceRef{Digest: digestB},
		}},
	}
	raw, _ := json.Marshal(sub)
	rr := doReq(t, srv, "POST", "/api/v1/evaluations", "dev", string(raw))
	if rr.Code != http.StatusPreconditionFailed {
		t.Fatalf("POST = %d, want 412: %s", rr.Code, rr.Body.String())
	}
	var serr evaluation.SubmissionError
	if err := json.Unmarshal(rr.Body.Bytes(), &serr); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if len(serr.MissingWorkspaces) != 1 || serr.MissingWorkspaces[0] != digestB {
		t.Errorf("missing = %v, want [%s]", serr.MissingWorkspaces, digestB)
	}
}

func settledUnit(evalName string, index int32) *v1alpha1.EvaluationUnit {
	return &v1alpha1.EvaluationUnit{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-u%03d", evalName, index),
			Namespace: "patchy",
			Labels:    map[string]string{v1alpha1.LabelEvaluation: evalName},
		},
		Spec: v1alpha1.EvaluationUnitSpec{
			EvaluationRef: v1alpha1.ObjectReference{Name: evalName},
			Index:         index,
			Unit: v1alpha1.UnitPlan{
				Skill: "s", Tier: 2, Model: "anthropic/claude-sonnet-5",
				Harnesses: []v1alpha1.HarnessOption{{Harness: "claude"}},
				Workspace: v1alpha1.WorkspaceRef{Digest: digestA},
			},
		},
		Status: v1alpha1.EvaluationUnitStatus{
			Phase:       v1alpha1.RunComplete,
			Harness:     "claude",
			CasesPassed: 2,
			Usage:       v1alpha1.UsageSummary{CostUSD: "1.500000"},
		},
	}
}

func terminalEval(name string) *v1alpha1.Evaluation {
	return &v1alpha1.Evaluation{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "patchy"},
		Spec: v1alpha1.EvaluationSpec{
			Submitter: "dev",
			Units: []v1alpha1.UnitPlan{{
				Skill: "s", Tier: 2, Model: "anthropic/claude-sonnet-5",
				Harnesses: []v1alpha1.HarnessOption{{Harness: "claude"}},
				Workspace: v1alpha1.WorkspaceRef{Digest: digestA},
			}},
		},
		Status: v1alpha1.EvaluationStatus{
			Phase: v1alpha1.EvaluationComplete, Units: 1, UnitsComplete: 1,
		},
	}
}

func TestSnapshotLiftsSummaries(t *testing.T) {
	srv, _, _ := newTestServer(t, terminalEval("eval-x"), settledUnit("eval-x", 0))
	rr := doReq(t, srv, "GET", "/api/v1/evaluations/eval-x", "dev", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET = %d: %s", rr.Code, rr.Body.String())
	}
	var snap evaluation.EvaluationStatusWire
	if err := json.Unmarshal(rr.Body.Bytes(), &snap); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if snap.Phase != "Complete" || len(snap.Units) != 1 {
		t.Fatalf("snapshot = %+v", snap)
	}
	u := snap.Units[0]
	if u.Summary == nil || u.Summary.CasesPassed != 2 || u.Summary.TokenUsage.CostUSD != 1.5 {
		t.Errorf("unit summary = %+v", u.Summary)
	}
	if u.Result == nil || u.Result.Model != "anthropic/claude-sonnet-5" {
		t.Errorf("unit result = %+v", u.Result)
	}
}

func TestSnapshotMissingIs404(t *testing.T) {
	srv, _, _ := newTestServer(t)
	if rr := doReq(t, srv, "GET", "/api/v1/evaluations/absent", "dev", ""); rr.Code != http.StatusNotFound {
		t.Errorf("GET absent = %d, want 404", rr.Code)
	}
}

func TestCancel(t *testing.T) {
	srv, c, _ := newTestServer(t, terminalEval("eval-x"))
	if rr := doReq(t, srv, "DELETE", "/api/v1/evaluations/eval-x", "dev", ""); rr.Code != http.StatusAccepted {
		t.Errorf("DELETE = %d, want 202", rr.Code)
	}
	var eval v1alpha1.Evaluation
	if err := c.Get(t.Context(), types.NamespacedName{Namespace: "patchy", Name: "eval-x"}, &eval); err == nil {
		t.Error("evaluation still present after cancel")
	}
	if rr := doReq(t, srv, "DELETE", "/api/v1/evaluations/eval-x", "dev", ""); rr.Code != http.StatusNotFound {
		t.Errorf("DELETE absent = %d, want 404", rr.Code)
	}
}

func TestSSEReplayAndEnd(t *testing.T) {
	srv, _, _ := newTestServer(t, terminalEval("eval-x"), settledUnit("eval-x", 0))

	req := httptest.NewRequest("GET", "/api/v1/evaluations/eval-x/events", nil)
	req.Header.Set("X-Test-User", "dev")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("SSE = %d: %s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type = %q", ct)
	}

	var events []string
	sc := bufio.NewScanner(strings.NewReader(rr.Body.String()))
	for sc.Scan() {
		if name, found := strings.CutPrefix(sc.Text(), "event: "); found {
			events = append(events, name)
		}
	}
	want := []string{evaluation.SSEEventUnit, evaluation.SSEEventEnd}
	if len(events) != len(want) || events[0] != want[0] || events[1] != want[1] {
		t.Errorf("events = %v, want %v", events, want)
	}
}
