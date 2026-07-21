// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package agentrun

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bitwise-media-group/patchy/internal/envelope"
	"github.com/bitwise-media-group/patchy/internal/runner"
)

// Captured claude stream-json shapes.
const (
	testSessionID = "5e3f9a1c-8b2d-4f6e-9c7a-1d2e3f4a5b6c"

	streamSuccess = `{"type":"system","subtype":"init","session_id":"` + testSessionID + `"}` + "\n" +
		`{"type":"assistant","message":{"usage":{"output_tokens":12},` +
		`"content":[{"type":"text","text":"Working."}]}}` + "\n" +
		`{"type":"result","subtype":"success","is_error":false,"result":"Done.",` +
		`"session_id":"` + testSessionID + `","num_turns":7,"total_cost_usd":0.0123,` +
		`"usage":{"input_tokens":100,"cache_creation_input_tokens":20,` +
		`"cache_read_input_tokens":50,"output_tokens":30}}`

	streamExecError = `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"",` +
		`"errors":["boom"]}`
)

const goodInvestigation = `---
exploitability:
  rating: high
  summary: reachable from the request path
likelihood:
  rating: medium
  summary: requires an authenticated caller
impact:
  rating: high
  summary: full table read
recommendation: remediate
priority: high
severity: high
confidence: 0.9
breaking_change_available: false
model: claude-sonnet-5
max_turns: 40
token_budget: 200000
---
Real finding; fix is mechanical.
`

const goodRemediation = `---
success: true
confidence: 0.88
---
Escaped the sink.
`

// fakeExec scripts one stage at a time: each call pops the next step,
// writing its files and returning its canned runner.Result.
type fakeExec struct {
	steps []step
	specs []runner.CommandSpec
}

type step struct {
	// writes maps workspace-relative paths to contents produced by the agent.
	writes map[string]string
	stdout string
	result runner.Result
	// repoWrite optionally dirties the repo working tree (a "fix").
	repoWrite map[string]string
	ws        string
	// budgetLines are streamed to this stage's usage observer, exercising
	// the token-budget kill switch.
	budgetLines []string
}

func (f *fakeExec) Run(_ context.Context, spec runner.CommandSpec, _ time.Duration,
	onLine func([]byte) (bool, string)) (runner.Result, error) {
	f.specs = append(f.specs, spec)
	if len(f.steps) == 0 {
		return runner.Result{}, fmt.Errorf("fakeExec: no step scripted for call %d", len(f.specs))
	}
	s := f.steps[0]
	f.steps = f.steps[1:]

	for path, content := range s.writes {
		full := filepath.Join(s.ws, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return runner.Result{}, err
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			return runner.Result{}, err
		}
	}
	for path, content := range s.repoWrite {
		full := filepath.Join(spec.Dir, path)
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			return runner.Result{}, err
		}
	}

	if onLine != nil {
		for _, line := range s.budgetLines {
			if abort, reason := onLine([]byte(line)); abort {
				return runner.Result{Aborted: true, AbortReason: reason, Stdout: []byte(s.stdout)}, nil
			}
		}
	}

	res := s.result
	if res.Stdout == nil {
		res.Stdout = []byte(s.stdout)
	}
	if res.Elapsed == 0 {
		res.Elapsed = 3 * time.Second
	}
	return res, nil
}

// newWorkspace builds the pod layout the init container would assemble: a
// git repo with one commit, plus the issue handoff.
func newWorkspace(t *testing.T) string {
	t.Helper()
	ws := t.TempDir()
	repo := filepath.Join(ws, "repo")
	if err := os.MkdirAll(filepath.Join(ws, "input"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "test")
	// The machine's global config may mandate signed commits (hardware key);
	// test repos must not inherit that.
	run("config", "commit.gpgsign", "false")
	run("config", "tag.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(repo, "app.js"), []byte("vulnerable();\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "initial")

	if err := os.WriteFile(filepath.Join(ws, "input", "issue.md"), []byte("# finding"), 0o644); err != nil {
		t.Fatal(err)
	}
	return ws
}

func newConfig(t *testing.T, ws string, out io.Writer) Config {
	t.Helper()
	return Config{
		Workspace: ws, Repo: "acme/shop", Finding: "finding-abc123def0-1", Phase: PhaseInvestigate,
		InvestigateHarness: "fake", RemediateHarness: "fake",
		InvestigateModel: "claude-sonnet-5", RemediateModel: "claude-sonnet-5",
		ModelAllowlist:         []string{"claude-sonnet-5"},
		InvestigateMaxTurns:    25,
		InvestigateTokenBudget: 150000,
		RemediateMaxTurns:      80,
		RemediateTokenBudget:   400000,
		InvestigateTimeout:     time.Minute, RemediateTimeout: time.Minute,
		ChangesetMaxBytes: 5 << 20,
		Out:               out,
		Log:               slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// events decodes every envelope line from the runner's stdout.
func events(t *testing.T, out string) []envelope.Event {
	t.Helper()
	var evs []envelope.Event
	for _, line := range strings.Split(out, "\n") {
		if e, ok := envelope.Decode([]byte(line)); ok {
			evs = append(evs, e)
		}
	}
	return evs
}

// commitScript is a well-behaved commit.sh honoring the documented contract.
const commitScript = "#!/bin/sh\nset -e\ngit add app.js\ngit commit -m 'fix(security): escape sink'\n"

// investigateRun drives the analysis happy path and returns its single event.
func investigateRun(t *testing.T) envelope.Event {
	t.Helper()
	ws := newWorkspace(t)
	var out bytes.Buffer
	fx := &fakeExec{steps: []step{
		{ws: ws, stdout: streamSuccess, writes: map[string]string{
			"reports/investigation.md": goodInvestigation,
		}},
	}}

	if err := New(newConfig(t, ws, &out), fx).Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	evs := events(t, out.String())
	if len(evs) != 1 {
		t.Fatalf("events = %d, want 1 (investigation):\n%s", len(evs), out.String())
	}
	if evs[0].Type != envelope.TypeInvestigation {
		t.Fatalf("event type = %q, want investigation", evs[0].Type)
	}
	return evs[0]
}

// remediateRun drives the remediation happy path — the controller-provided
// analysis, the fix, commit.sh, the changeset — and returns the workspace
// and the single remediation event.
func remediateRun(t *testing.T) (string, envelope.Event) {
	t.Helper()
	ws := newWorkspace(t)
	if err := os.WriteFile(filepath.Join(ws, "input", "investigation.md"),
		[]byte(goodInvestigation), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	cfg := newConfig(t, ws, &out)
	cfg.Phase = PhaseRemediate
	fx := &fakeExec{steps: []step{
		{ws: ws, stdout: streamSuccess,
			writes:    map[string]string{"reports/remediation.md": goodRemediation, "commit.sh": commitScript},
			repoWrite: map[string]string{"app.js": "escaped();\n"},
		},
	}}

	if err := New(cfg, fx).Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	evs := events(t, out.String())
	if len(evs) != 1 || evs[0].Type != envelope.TypeRemediation {
		t.Fatalf("events = %+v, want only a remediation event", evs)
	}
	return ws, evs[0]
}

func TestInvestigationEvent(t *testing.T) {
	inv := investigateRun(t).Investigation

	if inv.Outcome != envelope.OutcomeOK {
		t.Fatalf("outcome = %q (detail: %q)", inv.Outcome, inv.Detail)
	}
	if inv.Recommendation != "remediate" || inv.Confidence != 0.9 {
		t.Errorf("verdict = %s/%v, want remediate/0.9", inv.Recommendation, inv.Confidence)
	}
	if inv.Exploitability.Rating != "high" || inv.Likelihood.Rating != "medium" || inv.Impact.Rating != "high" {
		t.Errorf("dimensions = %s/%s/%s, want high/medium/high",
			inv.Exploitability.Rating, inv.Likelihood.Rating, inv.Impact.Rating)
	}
	if inv.RemediationModel != "claude-sonnet-5" || inv.MaxTurns != 40 || inv.TokenBudget != 200000 {
		t.Errorf("stage-2 params = %q/%d/%d, want the report's (in-bounds) values",
			inv.RemediationModel, inv.MaxTurns, inv.TokenBudget)
	}
	if inv.AwaitApproval {
		t.Error("AwaitApproval = true without a breaking-change hold")
	}
}

func TestInvestigationReportsAccounting(t *testing.T) {
	inv := investigateRun(t).Investigation

	if inv.SessionID != testSessionID || inv.NumTurns != 7 {
		t.Errorf("session/turns = %q/%d, want %q/7", inv.SessionID, inv.NumTurns, testSessionID)
	}
	if inv.Usage.InputTokens != 100 || inv.Usage.OutputTokens != 30 {
		t.Errorf("tokens = %d in / %d out, want 100/30", inv.Usage.InputTokens, inv.Usage.OutputTokens)
	}
	if inv.Usage.CacheReadTokens != 50 || inv.Usage.CacheCreationTokens != 20 {
		t.Errorf("cache tokens = %d read / %d created, want 50/20",
			inv.Usage.CacheReadTokens, inv.Usage.CacheCreationTokens)
	}
	if inv.Usage.CostUSD != 0.0123 {
		t.Errorf("CostUSD = %v, want 0.0123", inv.Usage.CostUSD)
	}
	if inv.ElapsedSeconds == 0 {
		t.Error("ElapsedSeconds = 0, want the stage's wall clock")
	}
}

func TestRemediatePackagesChangeset(t *testing.T) {
	ws, ev := remediateRun(t)
	rem := ev.Remediation

	if rem.Outcome != envelope.OutcomeOK || !rem.Success {
		t.Fatalf("remediation = %q/success:%v (detail: %q)", rem.Outcome, rem.Success, rem.Detail)
	}
	if rem.Branch != "patchy/finding-abc123def0-1" {
		t.Errorf("Branch = %q, want patchy/finding-abc123def0-1", rem.Branch)
	}
	if rem.Confidence != 0.88 {
		t.Errorf("Confidence = %v, want 0.88", rem.Confidence)
	}

	cs := rem.Changeset
	if cs == nil {
		t.Fatal("Changeset is nil; the controller has nothing to push")
	}
	if want := headOf(t, filepath.Join(ws, "repo"), "main"); cs.BaseSHA != want {
		t.Errorf("BaseSHA = %q, want the pinned clone head %q", cs.BaseSHA, want)
	}
	if !strings.Contains(cs.CommitMessage, "fix(security): escape sink") {
		t.Errorf("CommitMessage = %q, want the agent's commit message", cs.CommitMessage)
	}
	if len(cs.Upserts) != 1 || len(cs.Deletes) != 0 {
		t.Fatalf("changeset = %d upserts / %d deletes, want 1/0", len(cs.Upserts), len(cs.Deletes))
	}
	up := cs.Upserts[0]
	if up.Path != "app.js" || up.Mode != "100644" {
		t.Errorf("upsert = %s (%s), want app.js (100644)", up.Path, up.Mode)
	}
	if got := decodeB64(t, up.ContentB64); got != "escaped();\n" {
		t.Errorf("content = %q, want the fixed file", got)
	}
}

// The remote base SHA overrides the synthetic local base in artifact mode.
func TestRemediateStampsRemoteBaseSHA(t *testing.T) {
	ws := newWorkspace(t)
	if err := os.WriteFile(filepath.Join(ws, "input", "investigation.md"),
		[]byte(goodInvestigation), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	cfg := newConfig(t, ws, &out)
	cfg.Phase = PhaseRemediate
	cfg.BaseSHA = "feedface00feedface00feedface00feedface00"
	fx := &fakeExec{steps: []step{
		{ws: ws, stdout: streamSuccess,
			writes:    map[string]string{"reports/remediation.md": goodRemediation, "commit.sh": commitScript},
			repoWrite: map[string]string{"app.js": "escaped();\n"},
		},
	}}

	if err := New(cfg, fx).Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	cs := events(t, out.String())[0].Remediation.Changeset
	if cs == nil || cs.BaseSHA != cfg.BaseSHA {
		t.Fatalf("changeset base = %+v, want the remote SHA %s", cs, cfg.BaseSHA)
	}
}

// headOf resolves a ref in a test repository.
func headOf(t *testing.T, repo, ref string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", ref)
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse %s: %v", ref, err)
	}
	return strings.TrimSpace(string(out))
}

func decodeB64(t *testing.T, s string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("base64: %v", err)
	}
	return string(raw)
}

func TestInvestigationVerdicts(t *testing.T) {
	frontmatter := func(recommendation string, confidence float64, breaking bool) string {
		return fmt.Sprintf(`---
exploitability: {rating: high, summary: reachable}
likelihood: {rating: medium, summary: authenticated only}
impact: {rating: high, summary: data read}
recommendation: %s
priority: high
severity: high
confidence: %v
breaking_change_available: %v
model: claude-sonnet-5
max_turns: 40
token_budget: 200000
---
analysis
`, recommendation, confidence, breaking)
	}

	tests := []struct {
		name         string
		report       string
		wantVerdict  string
		wantApproval bool
	}{
		{"remediate", frontmatter("remediate", 0.9, false), "remediate", false},
		{"breaking change holds", frontmatter("remediate", 0.95, true), "remediate", true},
		{"ignore", frontmatter("ignore", 0.99, false), "ignore", false},
		{"manual", frontmatter("manual", 0.9, false), "manual", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := newWorkspace(t)
			var out bytes.Buffer
			fx := &fakeExec{steps: []step{
				{ws: ws, stdout: streamSuccess, writes: map[string]string{"reports/investigation.md": tt.report}},
			}}

			if err := New(newConfig(t, ws, &out), fx).Run(context.Background()); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			evs := events(t, out.String())
			// The runner never continues to stage 2 — one event, always.
			if len(evs) != 1 {
				t.Fatalf("events = %d, want 1", len(evs))
			}
			inv := evs[0].Investigation
			if inv.Recommendation != tt.wantVerdict {
				t.Errorf("Recommendation = %q, want %q", inv.Recommendation, tt.wantVerdict)
			}
			if inv.AwaitApproval != tt.wantApproval {
				t.Errorf("AwaitApproval = %v, want %v", inv.AwaitApproval, tt.wantApproval)
			}
		})
	}
}

func TestInvestigationFailures(t *testing.T) {
	tests := []struct {
		name string
		step step
		want envelope.Outcome
	}{
		{
			name: "runtime error",
			step: step{stdout: streamExecError},
			want: envelope.OutcomeRuntimeError,
		},
		{
			name: "timeout",
			step: step{stdout: "", result: runner.Result{TimedOut: true}},
			want: envelope.OutcomeTimeout,
		},
		{
			name: "report missing",
			step: step{stdout: streamSuccess},
			want: envelope.OutcomeReportMissing,
		},
		{
			name: "report invalid",
			step: step{stdout: streamSuccess, writes: map[string]string{
				"reports/investigation.md": "---\nrecommendation: nonsense\n---\n",
			}},
			want: envelope.OutcomeReportInvalid,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := newWorkspace(t)
			var out bytes.Buffer
			s := tt.step
			s.ws = ws
			fx := &fakeExec{steps: []step{s}}

			if err := New(newConfig(t, ws, &out), fx).Run(context.Background()); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			evs := events(t, out.String())
			if len(evs) != 1 {
				t.Fatalf("events = %d, want 1", len(evs))
			}
			if got := evs[0].Investigation.Outcome; got != tt.want {
				t.Errorf("outcome = %q, want %q (detail: %q)", got, tt.want, evs[0].Investigation.Detail)
			}
		})
	}
}

// remediateConfig builds a PhaseRemediate config over a workspace seeded
// with the given analysis handoff.
func remediateConfig(t *testing.T, analysis string, out io.Writer) (Config, string) {
	t.Helper()
	ws := newWorkspace(t)
	if analysis != "" {
		if err := os.WriteFile(filepath.Join(ws, "input", "investigation.md"),
			[]byte(analysis), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := newConfig(t, ws, out)
	cfg.Phase = PhaseRemediate
	return cfg, ws
}

func TestRemediationDowngradedWhenCommitScriptMissing(t *testing.T) {
	var out bytes.Buffer
	cfg, ws := remediateConfig(t, goodInvestigation, &out)
	fx := &fakeExec{steps: []step{
		// Claims success but writes no commit.sh: the repository is the
		// source of truth, so the claim is downgraded.
		{ws: ws, stdout: streamSuccess, writes: map[string]string{"reports/remediation.md": goodRemediation}},
	}}

	if err := New(cfg, fx).Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	rem := events(t, out.String())[0].Remediation
	if rem.Success {
		t.Error("Success = true despite a missing commit.sh")
	}
	if rem.Outcome != envelope.OutcomeCommitFailed {
		t.Errorf("outcome = %q, want commit_failed", rem.Outcome)
	}
}

func TestRemediationDowngradedWhenNothingCommitted(t *testing.T) {
	var out bytes.Buffer
	cfg, ws := remediateConfig(t, goodInvestigation, &out)
	// A commit.sh that stages nothing leaves no commits on the branch.
	emptyScript := "#!/bin/sh\nexit 0\n"
	fx := &fakeExec{steps: []step{
		{ws: ws, stdout: streamSuccess, writes: map[string]string{
			"reports/remediation.md": goodRemediation, "commit.sh": emptyScript,
		}},
	}}

	if err := New(cfg, fx).Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	rem := events(t, out.String())[0].Remediation
	if rem.Success || rem.Outcome != envelope.OutcomeCommitFailed {
		t.Errorf("remediation = %+v, want downgraded commit_failed", rem)
	}
	if !strings.Contains(rem.Detail, "no commits") {
		t.Errorf("Detail = %q, want the empty-branch explanation", rem.Detail)
	}
}

func TestRemediationReportsFailureHonestly(t *testing.T) {
	var out bytes.Buffer
	cfg, ws := remediateConfig(t, goodInvestigation, &out)
	failed := "---\nsuccess: false\nconfidence: 0.2\n---\nCould not fix safely.\n"
	fx := &fakeExec{steps: []step{
		{ws: ws, stdout: streamSuccess, writes: map[string]string{"reports/remediation.md": failed}},
	}}

	if err := New(cfg, fx).Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	rem := events(t, out.String())[0].Remediation
	// The stage itself ran fine; the agent simply could not fix it.
	if rem.Outcome != envelope.OutcomeOK || rem.Success {
		t.Errorf("remediation = %+v, want outcome ok with success=false", rem)
	}
	if rem.Changeset != nil {
		t.Error("a failed remediation must not carry a changeset")
	}
}

func TestRemediationFatalWithoutAnalysis(t *testing.T) {
	var out bytes.Buffer
	cfg, _ := remediateConfig(t, "", &out)
	err := New(cfg, &fakeExec{}).Run(context.Background())
	if err == nil {
		t.Fatal("Run() error = nil, want a fatal error")
	}
	evs := events(t, out.String())
	if len(evs) != 1 || evs[0].Type != envelope.TypeFatal {
		t.Fatalf("events = %+v, want one fatal event", evs)
	}
}

func TestBudgetKillSwitch(t *testing.T) {
	var out bytes.Buffer
	cfg, ws := remediateConfig(t, goodInvestigation, &out)
	fx := &fakeExec{steps: []step{
		// Two assistant events at 150k output tokens each blow the 200k
		// budget the investigation asked for.
		{ws: ws, stdout: streamSuccess, budgetLines: []string{
			`{"type":"assistant","message":{"usage":{"output_tokens":150000}}}`,
			`{"type":"assistant","message":{"usage":{"output_tokens":150000}}}`,
		}},
	}}

	if err := New(cfg, fx).Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	rem := events(t, out.String())[0].Remediation
	if rem.Outcome != envelope.OutcomeBudgetExceeded {
		t.Fatalf("outcome = %q, want budget_exceeded", rem.Outcome)
	}
	if !strings.Contains(rem.Detail, "budget exceeded") {
		t.Errorf("Detail = %q", rem.Detail)
	}
	if rem.Success {
		t.Error("an aborted remediation must not report success")
	}
}

// The investigation stage carries its own budget: an agent that burns
// through it is killed before its report is trusted.
func TestInvestigateBudgetKillSwitch(t *testing.T) {
	ws := newWorkspace(t)
	var out bytes.Buffer
	cfg := newConfig(t, ws, &out)
	cfg.InvestigateTokenBudget = 100000
	fx := &fakeExec{steps: []step{
		{ws: ws, stdout: streamSuccess,
			writes: map[string]string{"reports/investigation.md": goodInvestigation},
			budgetLines: []string{
				`{"type":"assistant","message":{"usage":{"output_tokens":60000}}}`,
				`{"type":"assistant","message":{"usage":{"output_tokens":60000}}}`,
			}},
	}}

	if err := New(cfg, fx).Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	evs := events(t, out.String())
	if len(evs) != 1 {
		t.Fatalf("events = %d, want 1", len(evs))
	}
	inv := evs[0].Investigation
	if inv.Outcome != envelope.OutcomeBudgetExceeded {
		t.Errorf("outcome = %q, want budget_exceeded", inv.Outcome)
	}
	if !strings.Contains(inv.Detail, "budget exceeded") {
		t.Errorf("Detail = %q", inv.Detail)
	}
}

func TestClampsRogueInvestigation(t *testing.T) {
	ws := newWorkspace(t)
	var out bytes.Buffer
	rogue := `---
exploitability: {rating: high, summary: reachable}
likelihood: {rating: medium, summary: authenticated only}
impact: {rating: high, summary: data read}
recommendation: remediate
priority: high
severity: high
confidence: 0.9
breaking_change_available: false
model: evil-model-9000
max_turns: 100000
token_budget: 99999999
---
analysis
`
	fx := &fakeExec{steps: []step{
		{ws: ws, stdout: streamSuccess, writes: map[string]string{"reports/investigation.md": rogue}},
	}}

	if err := New(newConfig(t, ws, &out), fx).Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	inv := events(t, out.String())[0].Investigation
	if inv.RemediationModel != "claude-sonnet-5" {
		t.Errorf("model = %q, want the allowlisted default", inv.RemediationModel)
	}
	if inv.MaxTurns != 80 {
		t.Errorf("max_turns = %d, want clamped to the 80 ceiling", inv.MaxTurns)
	}
	if inv.TokenBudget != 400000 {
		t.Errorf("token_budget = %d, want clamped to the 400000 ceiling", inv.TokenBudget)
	}
}

func TestFatalWhenWorkspaceIncomplete(t *testing.T) {
	var out bytes.Buffer
	cfg := newConfig(t, t.TempDir(), &out) // no repo clone, no issue handoff
	err := New(cfg, &fakeExec{}).Run(context.Background())
	if err == nil {
		t.Fatal("Run() error = nil, want a fatal error")
	}
	evs := events(t, out.String())
	if len(evs) != 1 || evs[0].Type != envelope.TypeFatal {
		t.Fatalf("events = %+v, want one fatal event", evs)
	}
	if evs[0].Repo != "acme/shop" || evs[0].Finding != "finding-abc123def0-1" {
		t.Errorf("fatal event lacks finding context: %+v", evs[0])
	}
}
