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

const goodClassification = `---
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
	// the token-budget kill switch. Both stages have one.
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

// newWorkspace builds the pod layout the controller would assemble: a git
// repo clone with one commit, plus the issue handoff.
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
		Workspace: ws, Repo: "acme/shop", Issue: 123, Phase: PhaseFull,
		ClassifyHarness: "fake", RemediateHarness: "fake",
		ClassifyModel: "claude-sonnet-5", RemediateModel: "claude-sonnet-5",
		ModelAllowlist:       []string{"claude-sonnet-5"},
		ClassifyMaxTurns:     25,
		ClassifyTokenBudget:  150000,
		RemediateMaxTurns:    80,
		RemediateTokenBudget: 400000,
		ClassifyTimeout:      time.Minute, RemediateTimeout: time.Minute,
		ConfidenceThreshold: 0.75,
		ChangesetMaxBytes:   5 << 20,
		Out:                 out,
		Log:                 slog.New(slog.NewTextHandler(io.Discard, nil)),
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

// fullRun drives the happy path — classify, remediate, commit, changeset —
// and returns the workspace and the two events it emitted.
func fullRun(t *testing.T) (string, envelope.Event, envelope.Event) {
	t.Helper()
	ws := newWorkspace(t)
	var out bytes.Buffer
	fx := &fakeExec{steps: []step{
		{ws: ws, stdout: streamSuccess, writes: map[string]string{
			"reports/classification.md": goodClassification,
		}},
		{ws: ws, stdout: streamSuccess,
			writes:    map[string]string{"reports/remediation.md": goodRemediation, "commit.sh": commitScript},
			repoWrite: map[string]string{"app.js": "escaped();\n"},
		},
	}}

	if err := New(newConfig(t, ws, &out), fx).Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	evs := events(t, out.String())
	if len(evs) != 2 {
		t.Fatalf("events = %d, want 2 (classification, remediation):\n%s", len(evs), out.String())
	}
	if evs[0].Type != envelope.TypeClassification || evs[1].Type != envelope.TypeRemediation {
		t.Fatalf("event types = %q, %q", evs[0].Type, evs[1].Type)
	}
	return ws, evs[0], evs[1]
}

func TestFullRunClassificationEvent(t *testing.T) {
	_, ev, _ := fullRun(t)
	cls := ev.Classification

	if cls.Outcome != envelope.OutcomeOK {
		t.Fatalf("outcome = %q (detail: %q)", cls.Outcome, cls.Detail)
	}
	if !cls.WillRemediate || cls.Confidence != 0.9 || cls.Recommendation != "remediate" {
		t.Errorf("verdict = %s/%v/will-remediate:%v, want remediate/0.9/true",
			cls.Recommendation, cls.Confidence, cls.WillRemediate)
	}
	if cls.RemediationModel != "claude-sonnet-5" || cls.MaxTurns != 40 || cls.TokenBudget != 200000 {
		t.Errorf("stage-2 params = %q/%d/%d, want the report's (in-bounds) values",
			cls.RemediationModel, cls.MaxTurns, cls.TokenBudget)
	}
}

// The label set in DESIGN.md is derived from these fields, so the accounting
// must survive the harness → envelope hop intact.
func TestFullRunReportsAccounting(t *testing.T) {
	_, ev, _ := fullRun(t)
	cls := ev.Classification

	if cls.SessionID != testSessionID || cls.NumTurns != 7 {
		t.Errorf("session/turns = %q/%d, want %q/7", cls.SessionID, cls.NumTurns, testSessionID)
	}
	if cls.Usage.InputTokens != 100 || cls.Usage.OutputTokens != 30 {
		t.Errorf("tokens = %d in / %d out, want 100/30", cls.Usage.InputTokens, cls.Usage.OutputTokens)
	}
	if cls.Usage.CacheReadTokens != 50 || cls.Usage.CacheCreationTokens != 20 {
		t.Errorf("cache tokens = %d read / %d created, want 50/20",
			cls.Usage.CacheReadTokens, cls.Usage.CacheCreationTokens)
	}
	if cls.Usage.CostUSD != 0.0123 {
		t.Errorf("CostUSD = %v, want 0.0123", cls.Usage.CostUSD)
	}
	if cls.ElapsedSeconds == 0 {
		t.Error("ElapsedSeconds = 0, want the stage's wall clock")
	}
}

func TestFullRunPackagesChangeset(t *testing.T) {
	ws, _, ev := fullRun(t)
	rem := ev.Remediation

	if rem.Outcome != envelope.OutcomeOK || !rem.Success {
		t.Fatalf("remediation = %q/success:%v (detail: %q)", rem.Outcome, rem.Success, rem.Detail)
	}
	if rem.Branch != "patchy/issue-123" {
		t.Errorf("Branch = %q, want patchy/issue-123", rem.Branch)
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

func TestClassificationDecisions(t *testing.T) {
	frontmatter := func(recommendation string, confidence float64, breaking bool) string {
		return fmt.Sprintf(`---
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
		name          string
		report        string
		wantRemediate bool
		wantApproval  bool
	}{
		{"high confidence remediate", frontmatter("remediate", 0.9, false), true, false},
		{"at threshold remediates", frontmatter("remediate", 0.75, false), true, false},
		{"below threshold holds", frontmatter("remediate", 0.74, false), false, false},
		{"breaking change holds", frontmatter("remediate", 0.95, true), false, true},
		{"ignore stops", frontmatter("ignore", 0.99, false), false, false},
		{"manual stops", frontmatter("manual", 0.9, false), false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := newWorkspace(t)
			var out bytes.Buffer
			steps := []step{
				{ws: ws, stdout: streamSuccess, writes: map[string]string{"reports/classification.md": tt.report}},
			}
			if tt.wantRemediate {
				// Script the stage the verdict is expected to trigger.
				steps = append(steps, step{ws: ws, stdout: streamSuccess,
					writes:    map[string]string{"reports/remediation.md": goodRemediation, "commit.sh": commitScript},
					repoWrite: map[string]string{"app.js": "escaped();\n"},
				})
			}
			fx := &fakeExec{steps: steps}

			if err := New(newConfig(t, ws, &out), fx).Run(context.Background()); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			evs := events(t, out.String())
			cls := evs[0].Classification
			if cls.WillRemediate != tt.wantRemediate {
				t.Errorf("WillRemediate = %v, want %v", cls.WillRemediate, tt.wantRemediate)
			}
			if cls.AwaitApproval != tt.wantApproval {
				t.Errorf("AwaitApproval = %v, want %v", cls.AwaitApproval, tt.wantApproval)
			}
			// Exactly the expected stages ran: every scripted step consumed,
			// and a remediation event iff the verdict called for one.
			if len(fx.steps) != 0 {
				t.Errorf("%d scripted stages never ran", len(fx.steps))
			}
			wantEvents := 1
			if tt.wantRemediate {
				wantEvents = 2
			}
			if len(evs) != wantEvents {
				t.Fatalf("events = %d, want %d", len(evs), wantEvents)
			}
		})
	}
}

func TestClassificationFailures(t *testing.T) {
	tests := []struct {
		name   string
		step   step
		want   envelope.Outcome
		detail string
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
				"reports/classification.md": "---\nrecommendation: nonsense\n---\n",
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
			if got := evs[0].Classification.Outcome; got != tt.want {
				t.Errorf("outcome = %q, want %q (detail: %q)", got, tt.want, evs[0].Classification.Detail)
			}
			if evs[0].Classification.WillRemediate {
				t.Error("a failed classification must not proceed to remediation")
			}
		})
	}
}

func TestRemediationDowngradedWhenCommitScriptMissing(t *testing.T) {
	ws := newWorkspace(t)
	var out bytes.Buffer
	fx := &fakeExec{steps: []step{
		{ws: ws, stdout: streamSuccess, writes: map[string]string{"reports/classification.md": goodClassification}},
		// Claims success but writes no commit.sh: the repository is the
		// source of truth, so the claim is downgraded.
		{ws: ws, stdout: streamSuccess, writes: map[string]string{"reports/remediation.md": goodRemediation}},
	}}

	if err := New(newConfig(t, ws, &out), fx).Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	evs := events(t, out.String())
	rem := evs[len(evs)-1].Remediation
	if rem.Success {
		t.Error("Success = true despite a missing commit.sh")
	}
	if rem.Outcome != envelope.OutcomeCommitFailed {
		t.Errorf("outcome = %q, want commit_failed", rem.Outcome)
	}
}

func TestRemediationDowngradedWhenNothingCommitted(t *testing.T) {
	ws := newWorkspace(t)
	var out bytes.Buffer
	// A commit.sh that stages nothing leaves no commits on the branch.
	emptyScript := "#!/bin/sh\nexit 0\n"
	fx := &fakeExec{steps: []step{
		{ws: ws, stdout: streamSuccess, writes: map[string]string{"reports/classification.md": goodClassification}},
		{ws: ws, stdout: streamSuccess, writes: map[string]string{
			"reports/remediation.md": goodRemediation, "commit.sh": emptyScript,
		}},
	}}

	if err := New(newConfig(t, ws, &out), fx).Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	rem := events(t, out.String())[1].Remediation
	if rem.Success || rem.Outcome != envelope.OutcomeCommitFailed {
		t.Errorf("remediation = %+v, want downgraded commit_failed", rem)
	}
	if !strings.Contains(rem.Detail, "no commits") {
		t.Errorf("Detail = %q, want the empty-branch explanation", rem.Detail)
	}
}

func TestRemediationReportsFailureHonestly(t *testing.T) {
	ws := newWorkspace(t)
	var out bytes.Buffer
	failed := "---\nsuccess: false\nconfidence: 0.2\n---\nCould not fix safely.\n"
	fx := &fakeExec{steps: []step{
		{ws: ws, stdout: streamSuccess, writes: map[string]string{"reports/classification.md": goodClassification}},
		{ws: ws, stdout: streamSuccess, writes: map[string]string{"reports/remediation.md": failed}},
	}}

	if err := New(newConfig(t, ws, &out), fx).Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	rem := events(t, out.String())[1].Remediation
	// The stage itself ran fine; the agent simply could not fix it.
	if rem.Outcome != envelope.OutcomeOK || rem.Success {
		t.Errorf("remediation = %+v, want outcome ok with success=false", rem)
	}
	if rem.Changeset != nil {
		t.Error("a failed remediation must not carry a changeset")
	}
}

func TestBudgetKillSwitch(t *testing.T) {
	ws := newWorkspace(t)
	var out bytes.Buffer
	cfg := newConfig(t, ws, &out)
	fx := &fakeExec{steps: []step{
		{ws: ws, stdout: streamSuccess, writes: map[string]string{"reports/classification.md": goodClassification}},
		// Two assistant events at 150k output tokens each blow the 200k
		// budget the classification asked for.
		{ws: ws, stdout: streamSuccess, budgetLines: []string{
			`{"type":"assistant","message":{"usage":{"output_tokens":150000}}}`,
			`{"type":"assistant","message":{"usage":{"output_tokens":150000}}}`,
		}},
	}}

	if err := New(cfg, fx).Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	rem := events(t, out.String())[1].Remediation
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

// The classification stage carries its own budget: an agent that burns
// through it is killed before it ever asks for a remediation.
func TestClassifyBudgetKillSwitch(t *testing.T) {
	ws := newWorkspace(t)
	var out bytes.Buffer
	cfg := newConfig(t, ws, &out)
	cfg.ClassifyTokenBudget = 100000
	fx := &fakeExec{steps: []step{
		{ws: ws, stdout: streamSuccess,
			writes: map[string]string{"reports/classification.md": goodClassification},
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
		t.Fatalf("events = %d, want 1 (an aborted classification must not remediate)", len(evs))
	}
	cls := evs[0].Classification
	if cls.Outcome != envelope.OutcomeBudgetExceeded {
		t.Errorf("outcome = %q, want budget_exceeded", cls.Outcome)
	}
	if !strings.Contains(cls.Detail, "budget exceeded") {
		t.Errorf("Detail = %q", cls.Detail)
	}
	// The report on disk must not be trusted: the run was cut short.
	if cls.WillRemediate {
		t.Error("an aborted classification proceeded to remediation")
	}
}

func TestClampsRogueClassification(t *testing.T) {
	ws := newWorkspace(t)
	var out bytes.Buffer
	rogue := `---
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
		{ws: ws, stdout: streamSuccess, writes: map[string]string{"reports/classification.md": rogue}},
		{ws: ws, stdout: streamSuccess},
	}}

	if err := New(newConfig(t, ws, &out), fx).Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	cls := events(t, out.String())[0].Classification
	if cls.RemediationModel != "claude-sonnet-5" {
		t.Errorf("model = %q, want the allowlisted default", cls.RemediationModel)
	}
	if cls.MaxTurns != 80 {
		t.Errorf("max_turns = %d, want clamped to the 80 ceiling", cls.MaxTurns)
	}
	if cls.TokenBudget != 400000 {
		t.Errorf("token_budget = %d, want clamped to the 400000 ceiling", cls.TokenBudget)
	}
}

func TestApprovePhaseSkipsClassification(t *testing.T) {
	ws := newWorkspace(t)
	// The controller supplies the classification it re-read from the issue.
	if err := os.WriteFile(filepath.Join(ws, "input", "classification.md"),
		[]byte(strings.Replace(goodClassification, "confidence: 0.9", "confidence: 0.4", 1)), 0o644); err != nil {
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
	// Below-threshold confidence is bypassed by fiat on the /approve path.
	if !evs[0].Remediation.Success {
		t.Errorf("remediation = %+v, want success", evs[0].Remediation)
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
	if evs[0].Repo != "acme/shop" || evs[0].Issue != 123 {
		t.Errorf("fatal event lacks issue context: %+v", evs[0])
	}
}
