// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package harness

import (
	"slices"
	"testing"
)

// codex exec --json emits one JSON event per line: thread.started carries the
// thread id, item.* events the agent's messages and tool items (reported at
// item.started then item.completed), and turn.completed the per-turn usage.
// This fixture mirrors a real capture, including a non-JSON ERROR log line
// interleaved with the stream.
const (
	codexThreadID = "0198c5c4-e1b2-7d3e-9c7a-1d2e3f4a5b6c"

	codexStreamSuccess = `{"type":"thread.started","thread_id":"` + codexThreadID + `"}` + "\n" +
		`{"type":"turn.started"}` + "\n" +
		`2026-07-23T16:20:48.507788Z ERROR codex_core::phase2: transient log line` + "\n" +
		`{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"I'll fix the finding."}}` + "\n" +
		`{"type":"item.started","item":{"id":"item_1","type":"command_execution",` +
		`"command":"ls -la","status":"in_progress"}}` + "\n" +
		`{"type":"item.completed","item":{"id":"item_1","type":"command_execution",` +
		`"command":"ls -la","aggregated_output":"total 0\n","exit_code":0,"status":"completed"}}` + "\n" +
		`{"type":"item.completed","item":{"id":"item_2","type":"agent_message","text":"Report written."}}` + "\n" +
		`{"type":"turn.completed","usage":{"input_tokens":61395,"cached_input_tokens":51328,"output_tokens":184}}`

	// A failed turn with no agent output: not usable.
	codexStreamTurnFailed = `{"type":"thread.started","thread_id":"` + codexThreadID + `"}` + "\n" +
		`{"type":"turn.started"}` + "\n" +
		`{"type":"error","message":"stream disconnected"}` + "\n" +
		`{"type":"turn.failed","error":{"message":"429 rate limited"}}`

	// A crash before any terminal turn event: the stream just stops.
	codexStreamTruncated = `{"type":"thread.started","thread_id":"` + codexThreadID + `"}` + "\n" +
		`{"type":"turn.started"}`
)

func TestCodexPromptSpec(t *testing.T) {
	c := NewCodex()
	spec := c.PromptSpec("/work/ws", PromptRequest{
		Prompt: "fix the finding",
		Model:  "gpt-5.2-codex",
		// No codex mapping exists for these; they must not leak into argv.
		SessionID:       codexThreadID,
		MaxTurns:        30,
		AllowedTools:    []string{"Read", "Edit"},
		DisallowedTools: []string{"WebSearch"},
		AddDirs:         []string{"/scratch"},
		Env:             []string{"OPENAI_API_KEY=k"},
	})
	want := []string{
		"codex", "exec", "fix the finding",
		"--json",
		"--skip-git-repo-check",
		"--sandbox", "danger-full-access",
		"--model", "gpt-5.2-codex",
	}
	if !slices.Equal(spec.Argv, want) {
		t.Errorf("Argv =\n%q\nwant\n%q", spec.Argv, want)
	}
	if spec.Dir != "/work/ws" {
		t.Errorf("Dir = %q, want the workspace", spec.Dir)
	}
	if !slices.Equal(spec.Env, []string{"OPENAI_API_KEY=k"}) {
		t.Errorf("Env = %q, want the request env", spec.Env)
	}
}

func TestCodexPromptSpecSystemPromptAppend(t *testing.T) {
	c := NewCodex()
	spec := c.PromptSpec("/ws", PromptRequest{
		Prompt:             "fix it",
		Model:              "m",
		SystemPromptAppend: "never push",
	})
	// codex exec has no system-prompt channel; the append folds into the prompt.
	if got := spec.Argv[2]; got != "never push\n\nfix it" {
		t.Errorf("prompt = %q, want the system append folded in", got)
	}
}

func TestCodexParseResultSuccess(t *testing.T) {
	c := NewCodex()
	res, ok := c.ParseResult([]byte(codexStreamSuccess))
	if !ok {
		t.Fatal("ok = false, want a parsed terminal turn")
	}
	if want := "I'll fix the finding.\nReport written."; res.FinalText != want {
		t.Errorf("FinalText = %q, want %q", res.FinalText, want)
	}
	if res.SessionID != codexThreadID {
		t.Errorf("SessionID = %q, want the thread id %q", res.SessionID, codexThreadID)
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
	// Codex reports the whole prompt as input with a cached subset; the Usage
	// contract wants fresh input split from cache reads: 61395-51328 = 10067.
	if got := derefInt(res.Usage.InputTokens); got != 10067 {
		t.Errorf("InputTokens = %d, want 10067", got)
	}
	if got := derefInt(res.Usage.CacheReadTokens); got != 51328 {
		t.Errorf("CacheReadTokens = %d, want 51328", got)
	}
	if got := derefInt(res.Usage.OutputTokens); got != 184 {
		t.Errorf("OutputTokens = %d, want 184", got)
	}
	// Codex reports tokens but never cost.
	if res.Usage.CostUSD != nil {
		t.Errorf("CostUSD = %v, want nil", *res.Usage.CostUSD)
	}
}

func TestCodexParseResultFallbacks(t *testing.T) {
	c := NewCodex()

	// No terminal turn event: raw stdout comes back as FinalText with ok=false.
	raw := "plain text answer\n"
	if res, ok := c.ParseResult([]byte(raw)); ok || res.FinalText != raw || res.Usage != nil {
		t.Errorf("ParseResult(plain) = (%+v, %v), want raw fallback with ok=false", res, ok)
	}
	if res, ok := c.ParseResult([]byte(codexStreamTruncated)); ok || res.FinalText != codexStreamTruncated {
		t.Errorf("ParseResult(truncated) = (%+v, %v), want raw fallback with ok=false", res, ok)
	}

	// A failed turn is terminal: the error envelope parses.
	res, ok := c.ParseResult([]byte(codexStreamTurnFailed))
	if !ok || !res.IsError {
		t.Fatalf("ParseResult(turn.failed) = (%+v, %v), want the error envelope parsed", res, ok)
	}
	if want := []string{"stream disconnected", "429 rate limited"}; !slices.Equal(res.Errors, want) {
		t.Errorf("Errors = %q, want %q", res.Errors, want)
	}
}

func TestCodexRuntimeError(t *testing.T) {
	c := NewCodex()
	tests := []struct {
		name     string
		stdout   string
		exitCode int
		timedOut bool
		want     string
	}{
		{"usable agent output", codexStreamSuccess, 0, false, ""},
		{"nonzero exit with agent output is usable", codexStreamSuccess, 1, false, ""},
		{"empty", "", 1, false, "empty CLI output"},
		{"plain text clean exit", "hello\n", 0, false, ""},
		{"failed turn", codexStreamTurnFailed, 1, false,
			"codex run error: stream disconnected; 429 rate limited"},
		{"truncated stream crash", codexStreamTruncated, 1, false, "codex produced no agent output"},
		{"timeout with no agent output", codexStreamTruncated, -1, true, "timed out with no agent output"},
	}
	for _, tt := range tests {
		if got := c.RuntimeError([]byte(tt.stdout), tt.exitCode, tt.timedOut); got != tt.want {
			t.Errorf("%s: RuntimeError = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestCodexScanUsage(t *testing.T) {
	c := NewCodex()
	tests := []struct {
		name string
		line string
		want int
		ok   bool
	}{
		{"turn.completed with usage",
			`{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":42}}`, 42, true},
		{"turn.completed without usage", `{"type":"turn.completed"}`, 0, false},
		{"agent message", `{"type":"item.completed","item":{"type":"agent_message","text":"hi"}}`, 0, false},
		{"garbage", "not json", 0, false},
	}
	for _, tt := range tests {
		got, ok := c.ScanUsage([]byte(tt.line))
		if got != tt.want || ok != tt.ok {
			t.Errorf("%s: ScanUsage = (%d, %v), want (%d, %v)", tt.name, got, ok, tt.want, tt.ok)
		}
	}
}
