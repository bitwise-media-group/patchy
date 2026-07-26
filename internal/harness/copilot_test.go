// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package harness

import (
	"slices"
	"strings"
	"testing"
)

// `copilot --output-format json` writes the session-event stream to stdout, one
// JSON object per line, and appends a terminal type:"result" event carrying the
// session id and the exit code. These fixtures follow the event schema the CLI
// ships (schemas/session-events.schema.json): assistant.message for the agent's
// text, assistant.usage per model call, assistant.turn_end per turn,
// session.error for a failed session.
const (
	copilotSessionID = "8f14e45f-ceea-467a-9c1e-3b0c6f2a7d51"

	copilotStreamSuccess = `{"type":"session.start","sessionId":"` + copilotSessionID +
		`","timestamp":"2026-07-26T09:00:00.000Z","data":{}}` + "\n" +
		`{"type":"assistant.turn_start","timestamp":"2026-07-26T09:00:01.000Z","data":{"turnId":"t1"}}` + "\n" +
		`{"type":"assistant.message","timestamp":"2026-07-26T09:00:02.000Z",` +
		`"data":{"messageId":"m1","content":"I'll investigate the finding."}}` + "\n" +
		`{"type":"assistant.usage","timestamp":"2026-07-26T09:00:03.000Z",` +
		`"data":{"model":"claude-sonnet-5","inputTokens":40000,"outputTokens":120,` +
		`"cacheReadTokens":30000,"cacheWriteTokens":2000}}` + "\n" +
		`{"type":"assistant.message","timestamp":"2026-07-26T09:00:04.000Z",` +
		`"data":{"messageId":"m2","content":"Report written."}}` + "\n" +
		`{"type":"assistant.usage","timestamp":"2026-07-26T09:00:05.000Z",` +
		`"data":{"model":"claude-sonnet-5","inputTokens":5000,"outputTokens":64,"cacheReadTokens":4000}}` + "\n" +
		`{"type":"assistant.turn_end","timestamp":"2026-07-26T09:00:06.000Z","data":{"turnId":"t1"}}` + "\n" +
		`{"type":"result","timestamp":"2026-07-26T09:00:07.000Z","sessionId":"` + copilotSessionID +
		`","exitCode":0,"usage":{"premiumRequests":1,"totalApiDurationMs":6000,"sessionDurationMs":7000}}`

	// A session error with no agent output: not usable.
	copilotStreamSessionError = `{"type":"session.start","sessionId":"` + copilotSessionID +
		`","timestamp":"2026-07-26T09:00:00.000Z","data":{}}` + "\n" +
		`{"type":"session.error","timestamp":"2026-07-26T09:00:01.000Z",` +
		`"data":{"errorType":"rate_limit","message":"user_weekly_rate_limited","statusCode":429}}` + "\n" +
		`{"type":"result","timestamp":"2026-07-26T09:00:02.000Z","sessionId":"` + copilotSessionID +
		`","exitCode":1,"usage":{"premiumRequests":0}}`

	// A crash before the terminal result event: the stream just stops.
	copilotStreamTruncated = `{"type":"session.start","sessionId":"` + copilotSessionID +
		`","timestamp":"2026-07-26T09:00:00.000Z","data":{}}` + "\n" +
		`{"type":"assistant.turn_start","timestamp":"2026-07-26T09:00:01.000Z","data":{"turnId":"t1"}}`

	// Auth is validated against the GitHub API before the stream opens, so a
	// bad credential is plain text on stdout with no JSON at all.
	copilotStreamAuthFailure = "Error: Authentication token found but could not be validated.\n\n" +
		"  Failed to fetch PAT user login (401): GitHub returned: Bad credentials\n"
)

func TestCopilotPromptSpec(t *testing.T) {
	c := NewCopilot()
	spec := c.PromptSpec("/work/ws", PromptRequest{
		Prompt:    "investigate the finding",
		Model:     "claude-sonnet-5",
		SessionID: copilotSessionID,
		Sandbox:   SandboxReadOnly,
		AddDirs:   []string{"/scratch"},
		// copilot has no turn ceiling; this must not leak into argv.
		MaxTurns: 30,
		Env:      []string{"COPILOT_GITHUB_TOKEN=t"},
	})
	want := []string{
		"copilot", "-p", "investigate the finding",
		"--model", "claude-sonnet-5",
		"--output-format", "json",
		"--allow-all-tools",
		"--no-ask-user",
		"--disable-builtin-mcps",
		"--no-remote",
		"--no-remote-export",
		"--no-auto-update",
		"--deny-tool", "url()",
		"--add-dir", "/scratch",
		"--session-id", copilotSessionID,
	}
	if !slices.Equal(spec.Argv, want) {
		t.Errorf("Argv =\n%q\nwant\n%q", spec.Argv, want)
	}
	if spec.Dir != "/work/ws" {
		t.Errorf("Dir = %q, want the workspace", spec.Dir)
	}
	if !slices.Equal(spec.Env, []string{"COPILOT_GITHUB_TOKEN=t"}) {
		t.Errorf("Env = %q, want the request env", spec.Env)
	}
}

// TestCopilotPromptSpecPodFlags guards the flags that exist for the pod rather
// than the prompt. Losing any of them silently widens what an agent pod can do
// with the GitHub token copilot authenticates with, or lets session content
// leave the pod, so they must be on every run regardless of posture.
func TestCopilotPromptSpecPodFlags(t *testing.T) {
	spec := NewCopilot().PromptSpec("/ws", PromptRequest{Prompt: "p", Model: "m"})
	for _, flag := range []string{
		"--disable-builtin-mcps", "--no-remote", "--no-remote-export",
		"--no-auto-update", "--allow-all-tools", "--no-ask-user",
	} {
		if !slices.Contains(spec.Argv, flag) {
			t.Errorf("Argv missing %s: %q", flag, spec.Argv)
		}
	}
	// SandboxDefault renders no deny grammar.
	if slices.Contains(spec.Argv, "--deny-tool") {
		t.Errorf("SandboxDefault rendered a deny rule: %q", spec.Argv)
	}
}

// TestCopilotPromptSpecDeniesURLs pins URL denial to both real postures: the
// pod has no egress and the stages never fetch.
func TestCopilotPromptSpecDeniesURLs(t *testing.T) {
	for _, sandbox := range []Sandbox{SandboxReadOnly, SandboxWorkspaceWrite} {
		spec := NewCopilot().PromptSpec("/ws", PromptRequest{Prompt: "p", Model: "m", Sandbox: sandbox})
		i := slices.Index(spec.Argv, "--deny-tool")
		if i < 0 || i+1 >= len(spec.Argv) || spec.Argv[i+1] != "url()" {
			t.Errorf("sandbox %v: want --deny-tool url(), got %q", sandbox, spec.Argv)
		}
	}
}

func TestCopilotPromptSpecSystemPromptAppend(t *testing.T) {
	spec := NewCopilot().PromptSpec("/ws", PromptRequest{
		Prompt:             "fix it",
		Model:              "m",
		SystemPromptAppend: "never push",
	})
	// copilot has no system-prompt channel; the append folds into the prompt.
	if got := spec.Argv[2]; got != "never push\n\nfix it" {
		t.Errorf("prompt = %q, want the system append folded in", got)
	}
}

func TestCopilotParseResultSuccess(t *testing.T) {
	c := NewCopilot()
	res, ok := c.ParseResult([]byte(copilotStreamSuccess))
	if !ok {
		t.Fatal("ok = false, want a parsed terminal result")
	}
	if want := "I'll investigate the finding.\nReport written."; res.FinalText != want {
		t.Errorf("FinalText = %q, want %q", res.FinalText, want)
	}
	if res.SessionID != copilotSessionID {
		t.Errorf("SessionID = %q, want %q", res.SessionID, copilotSessionID)
	}
	if res.NumTurns != 1 {
		t.Errorf("NumTurns = %d, want 1", res.NumTurns)
	}
	if res.IsError || len(res.Errors) != 0 {
		t.Errorf("envelope = (%v, %q), want a clean success", res.IsError, res.Errors)
	}
	if res.Usage == nil {
		t.Fatal("Usage = nil, want populated")
	}
	// Copilot reports inputTokens inclusive of cache traffic; the Usage contract
	// wants fresh input split out. Call one: 40000-30000-2000 = 8000; call two:
	// 5000-4000 = 1000. Total fresh 9000, cache reads 34000, cache writes 2000.
	if got := derefInt(res.Usage.InputTokens); got != 9000 {
		t.Errorf("InputTokens = %d, want 9000 (fresh only)", got)
	}
	if got := derefInt(res.Usage.CacheReadTokens); got != 34000 {
		t.Errorf("CacheReadTokens = %d, want 34000", got)
	}
	if got := derefInt(res.Usage.CacheCreationTokens); got != 2000 {
		t.Errorf("CacheCreationTokens = %d, want 2000", got)
	}
	if got := derefInt(res.Usage.OutputTokens); got != 184 {
		t.Errorf("OutputTokens = %d, want 184", got)
	}
	// Copilot prices in premium requests, never dollars.
	if res.Usage.CostUSD != nil {
		t.Errorf("CostUSD = %v, want nil", *res.Usage.CostUSD)
	}
}

func TestCopilotParseResultFallbacks(t *testing.T) {
	c := NewCopilot()

	// No terminal result event: raw stdout comes back as FinalText with ok=false.
	for _, raw := range []string{copilotStreamAuthFailure, copilotStreamTruncated} {
		if res, ok := c.ParseResult([]byte(raw)); ok || res.FinalText != raw || res.Usage != nil {
			t.Errorf("ParseResult(%.20q) = (%+v, %v), want raw fallback with ok=false", raw, res, ok)
		}
	}

	// A non-zero exit code on the result event is terminal and an error.
	res, ok := c.ParseResult([]byte(copilotStreamSessionError))
	if !ok || !res.IsError {
		t.Fatalf("ParseResult(session.error) = (%+v, %v), want the error envelope parsed", res, ok)
	}
	if want := []string{"user_weekly_rate_limited"}; !slices.Equal(res.Errors, want) {
		t.Errorf("Errors = %q, want %q", res.Errors, want)
	}
	// No assistant.usage events in that stream, so usage stays absent rather
	// than reporting a fabricated zero.
	if res.Usage != nil {
		t.Errorf("Usage = %+v, want nil when the stream reported none", res.Usage)
	}
}

func TestCopilotRuntimeError(t *testing.T) {
	c := NewCopilot()
	tests := []struct {
		name     string
		stdout   string
		exitCode int
		timedOut bool
		want     string
	}{
		{"usable agent output", copilotStreamSuccess, 0, false, ""},
		{"nonzero exit with agent output is usable", copilotStreamSuccess, 1, false, ""},
		{"empty", "", 1, false, "empty CLI output"},
		{"session error", copilotStreamSessionError, 1, false,
			"copilot run error: user_weekly_rate_limited"},
		{"timeout with no agent output", copilotStreamTruncated, -1, true,
			"timed out with no agent output"},
	}
	for _, tt := range tests {
		if got := c.RuntimeError([]byte(tt.stdout), tt.exitCode, tt.timedOut); got != tt.want {
			t.Errorf("%s: RuntimeError = %q, want %q", tt.name, got, tt.want)
		}
	}
}

// TestCopilotRuntimeErrorSurfacesPlainText covers the auth path specifically:
// copilot validates its GitHub token before opening the stream and fails in
// plain text, so the reason must reach the operator rather than collapsing to a
// bare "no result event".
func TestCopilotRuntimeErrorSurfacesPlainText(t *testing.T) {
	got := NewCopilot().RuntimeError([]byte(copilotStreamAuthFailure), 1, false)
	if !strings.HasPrefix(got, "copilot produced no result event: ") {
		t.Fatalf("RuntimeError = %q, want the no-result-event reason", got)
	}
	if !strings.Contains(got, "could not be validated") {
		t.Errorf("RuntimeError = %q, want the CLI's own message carried through", got)
	}
}

func TestCopilotScanUsage(t *testing.T) {
	c := NewCopilot()
	tests := []struct {
		name string
		line string
		want int
		ok   bool
	}{
		{"assistant.usage",
			`{"type":"assistant.usage","data":{"inputTokens":10,"outputTokens":42}}`, 42, true},
		{"assistant.usage without output tokens",
			`{"type":"assistant.usage","data":{"inputTokens":10}}`, 0, false},
		{"assistant message", `{"type":"assistant.message","data":{"content":"hi"}}`, 0, false},
		{"terminal result", `{"type":"result","sessionId":"s","exitCode":0}`, 0, false},
		{"garbage", "not json", 0, false},
	}
	for _, tt := range tests {
		got, ok := c.ScanUsage([]byte(tt.line))
		if got != tt.want || ok != tt.ok {
			t.Errorf("%s: ScanUsage = (%d, %v), want (%d, %v)", tt.name, got, ok, tt.want, tt.ok)
		}
	}
}

// TestCopilotEnvKeys guards the accepted credential channels. runnercfg
// validates --copilot-secret-env against this list at controller startup, so a
// name missing here makes that credential unconfigurable, and jobs.reservedEnv
// must reserve each one so it can only reach the pod by secretKeyRef.
func TestCopilotEnvKeys(t *testing.T) {
	got := NewCopilot().EnvKeys()
	want := []string{"COPILOT_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"}
	if !slices.Equal(got, want) {
		t.Errorf("EnvKeys() = %v, want %v", got, want)
	}
}
