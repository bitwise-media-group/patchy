// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package harness

import (
	"slices"
	"testing"
)

// Claude Code emits one JSON event per line under --output-format stream-json
// --verbose: assistant events carry message.usage, and a terminal
// type:"result" event carries the final answer, session id, turn count,
// usage, cost, and error envelope. These fixtures mirror that captured shape.
const (
	claudeSessionID = "5e3f9a1c-8b2d-4f6e-9c7a-1d2e3f4a5b6c"

	claudeStreamSuccess = `{"type":"system","subtype":"init","session_id":"` + claudeSessionID + `"}` + "\n" +
		`{"type":"assistant","message":{"usage":{"output_tokens":12},` +
		`"content":[{"type":"text","text":"Working."}]}}` + "\n" +
		`{"type":"assistant","message":{"usage":{"output_tokens":9},` +
		`"content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"go test"}}]}}` + "\n" +
		`{"type":"result","subtype":"success","is_error":false,"result":"Done.",` +
		`"session_id":"` + claudeSessionID + `","num_turns":7,"total_cost_usd":0.0123,` +
		`"usage":{"input_tokens":100,"cache_creation_input_tokens":20,` +
		`"cache_read_input_tokens":50,"output_tokens":30}}`

	// A max-turns run with a populated result: the CLI flags it is_error, but
	// the partial answer is usable.
	claudeStreamMaxTurnsPartial = `{"type":"system","subtype":"init"}` + "\n" +
		`{"type":"result","subtype":"error_max_turns","is_error":true,"result":"Partial fix applied.",` +
		`"session_id":"` + claudeSessionID + `","num_turns":30,"errors":["hit max turns"]}`

	// A max-turns run that produced nothing: not usable.
	claudeStreamMaxTurnsEmpty = `{"type":"system","subtype":"init"}` + "\n" +
		`{"type":"result","subtype":"error_max_turns","is_error":true,"result":"","errors":["hit max turns"]}`

	// A crash mid-run with an error envelope: not usable.
	claudeStreamExecError = `{"type":"system","subtype":"init"}` + "\n" +
		`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"",` +
		`"errors":["tool Bash crashed"," ","API overloaded"]}`
)

func TestClaudePromptSpecAllFlags(t *testing.T) {
	c := NewClaude()
	spec := c.PromptSpec("/work/ws", PromptRequest{
		Prompt:             "fix the finding",
		Model:              "claude-fable-5",
		SystemPromptAppend: "never push",
		SessionID:          claudeSessionID,
		MaxTurns:           30,
		Sandbox:            SandboxWorkspaceWrite,
		AddDirs:            []string{"/scratch", "/fixtures"},
		Env:                []string{"ANTHROPIC_API_KEY=k"},
	})
	want := []string{
		"claude", "-p", "fix the finding",
		"--model", "claude-fable-5",
		"--output-format", "stream-json",
		"--verbose",
		"--max-turns", "30",
		"--allowedTools", "Read Glob Grep Edit Write NotebookEdit Bash",
		"--disallowedTools", "WebFetch WebSearch",
		"--add-dir", "/scratch",
		"--add-dir", "/fixtures",
		"--session-id", claudeSessionID,
		"--append-system-prompt", "never push",
	}
	if !slices.Equal(spec.Argv, want) {
		t.Errorf("Argv =\n%q\nwant\n%q", spec.Argv, want)
	}
	if spec.Dir != "/work/ws" {
		t.Errorf("Dir = %q, want the workspace", spec.Dir)
	}
	if !slices.Equal(spec.Env, []string{"ANTHROPIC_API_KEY=k"}) {
		t.Errorf("Env = %q, want the request env", spec.Env)
	}
}

func TestClaudePromptSpecMinimal(t *testing.T) {
	c := NewClaude()
	spec := c.PromptSpec("/ws", PromptRequest{Prompt: "hi", Model: "m"})
	want := []string{"claude", "-p", "hi", "--model", "m", "--output-format", "stream-json", "--verbose"}
	// SandboxDefault (the zero value) imposes no tool grammar.
	if !slices.Equal(spec.Argv, want) {
		t.Errorf("Argv = %q, want no optional flags: %q", spec.Argv, want)
	}
}

func TestClaudePromptSpecReadOnly(t *testing.T) {
	c := NewClaude()
	spec := c.PromptSpec("/ws", PromptRequest{Prompt: "look", Model: "m", Sandbox: SandboxReadOnly})
	want := []string{
		"claude", "-p", "look", "--model", "m", "--output-format", "stream-json", "--verbose",
		"--allowedTools", "Read Glob Grep Write Bash(git log:*) Bash(git show:*) Bash(git blame:*) Bash(git diff:*)",
		"--disallowedTools", "WebFetch WebSearch Task",
	}
	if !slices.Equal(spec.Argv, want) {
		t.Errorf("Argv =\n%q\nwant\n%q", spec.Argv, want)
	}
}

func TestClaudeParseResultSuccess(t *testing.T) {
	c := NewClaude()
	res, ok := c.ParseResult([]byte(claudeStreamSuccess))
	if !ok {
		t.Fatal("ok = false, want a parsed result event")
	}
	if res.FinalText != "Done." {
		t.Errorf("FinalText = %q, want %q", res.FinalText, "Done.")
	}
	if res.SessionID != claudeSessionID {
		t.Errorf("SessionID = %q, want %q", res.SessionID, claudeSessionID)
	}
	if res.NumTurns != 7 {
		t.Errorf("NumTurns = %d, want 7", res.NumTurns)
	}
	if res.IsError || res.Subtype != "success" {
		t.Errorf("envelope = (%v, %q), want a clean success", res.IsError, res.Subtype)
	}
	if res.Usage == nil {
		t.Fatal("Usage = nil, want populated")
	}
	// Fresh input, cache read, and cache write stay on their own fields.
	if got := derefInt(res.Usage.InputTokens); got != 100 {
		t.Errorf("InputTokens = %d, want 100", got)
	}
	if got := derefInt(res.Usage.CacheReadTokens); got != 50 {
		t.Errorf("CacheReadTokens = %d, want 50", got)
	}
	if got := derefInt(res.Usage.CacheCreationTokens); got != 20 {
		t.Errorf("CacheCreationTokens = %d, want 20", got)
	}
	if got := derefInt(res.Usage.OutputTokens); got != 30 {
		t.Errorf("OutputTokens = %d, want 30", got)
	}
	if res.Usage.CostUSD == nil || *res.Usage.CostUSD != 0.0123 {
		t.Errorf("CostUSD = %v, want 0.0123", res.Usage.CostUSD)
	}
}

func TestClaudeParseResultFallbacks(t *testing.T) {
	c := NewClaude()

	// No result event: raw stdout comes back as FinalText with ok=false.
	raw := "plain text answer\n"
	if res, ok := c.ParseResult([]byte(raw)); ok || res.FinalText != raw || res.Usage != nil {
		t.Errorf("ParseResult(plain) = (%+v, %v), want raw fallback with ok=false", res, ok)
	}

	// A max-turns envelope with a partial answer still parses fully.
	res, ok := c.ParseResult([]byte(claudeStreamMaxTurnsPartial))
	if !ok || !res.IsError || res.Subtype != "error_max_turns" {
		t.Fatalf("ParseResult(max-turns) = (%+v, %v), want the error envelope parsed", res, ok)
	}
	if res.FinalText != "Partial fix applied." || res.NumTurns != 30 {
		t.Errorf("partial = (%q, %d turns), want the partial answer and turn count", res.FinalText, res.NumTurns)
	}
	if res.Usage != nil {
		t.Errorf("Usage = %+v, want nil when the result event carries none", res.Usage)
	}
}

func TestClaudeRuntimeError(t *testing.T) {
	c := NewClaude()
	tests := []struct {
		name     string
		stdout   string
		exitCode int
		timedOut bool
		want     string
	}{
		{"usable result", claudeStreamSuccess, 0, false, ""},
		{"max turns with partial result is usable", claudeStreamMaxTurnsPartial, 1, false, ""},
		{"empty", "", 1, false, "empty CLI output"},
		{"plain text clean exit", "hello\n", 0, false, ""},
		{"plain text crash", "boom\n", 1, false, "unparseable CLI output"},
		{"timeout with no result", "partial stream\n", -1, true, "timed out with no result event"},
		{"max turns empty result", claudeStreamMaxTurnsEmpty, 1, false,
			"claude run error (error_max_turns): hit max turns"},
		{"error during execution", claudeStreamExecError, 1, false,
			"claude run error (error_during_execution): tool Bash crashed; API overloaded"},
	}
	for _, tt := range tests {
		if got := c.RuntimeError([]byte(tt.stdout), tt.exitCode, tt.timedOut); got != tt.want {
			t.Errorf("%s: RuntimeError = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestClaudeScanUsage(t *testing.T) {
	c := NewClaude()
	tests := []struct {
		name string
		line string
		want int
		ok   bool
	}{
		{"assistant with usage",
			`{"type":"assistant","message":{"usage":{"output_tokens":42},"content":[]}}`, 42, true},
		{"assistant without usage",
			`{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}`, 0, false},
		{"result event top-level usage is not counted",
			`{"type":"result","result":"Done.","usage":{"output_tokens":30}}`, 0, false},
		{"system event", `{"type":"system","subtype":"init"}`, 0, false},
		{"garbage", "not json", 0, false},
	}
	for _, tt := range tests {
		got, ok := c.ScanUsage([]byte(tt.line))
		if got != tt.want || ok != tt.ok {
			t.Errorf("%s: ScanUsage = (%d, %v), want (%d, %v)", tt.name, got, ok, tt.want, tt.ok)
		}
	}
}

func derefInt(p *int) int {
	if p == nil {
		return -1
	}
	return *p
}
