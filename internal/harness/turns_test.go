// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package harness

import (
	"strings"
	"testing"

	"github.com/bitwise-media-group/patchy/internal/transcript"
)

// scanAll runs every line of a stream through a TurnScanner, as the runner's
// line observer does.
func scanAll(s TurnScanner, stream string) []transcript.Turn {
	var got []transcript.Turn
	for _, line := range strings.Split(stream, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		got = append(got, s.ScanTurns([]byte(line))...)
	}
	return got
}

func TestClaudeScanTurns(t *testing.T) {
	got := scanAll(NewClaude(), claudeStreamSuccess)

	want := []transcript.Turn{
		{Role: transcript.RoleSystem, Kind: transcript.KindNotice, Text: "session " + claudeSessionID + " started"},
		{Role: transcript.RoleAssistant, Kind: transcript.KindText, Text: "Working."},
		{Role: transcript.RoleAssistant, Kind: transcript.KindToolUse, Tool: "Bash", Text: "go test"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d turns, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Role != want[i].Role || got[i].Kind != want[i].Kind ||
			got[i].Tool != want[i].Tool || got[i].Text != want[i].Text {
			t.Errorf("turn %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestClaudeScanTurnsIgnoresResultEvent(t *testing.T) {
	// The result event's text is the report; the transcript is the
	// conversation, not a second copy of the outcome.
	line := `{"type":"result","subtype":"success","result":"Done.","session_id":"x"}`
	if got := NewClaude().ScanTurns([]byte(line)); got != nil {
		t.Errorf("ScanTurns(result) = %+v, want nil", got)
	}
}

func TestClaudeScanTurnsBlocks(t *testing.T) {
	tests := []struct {
		name string
		line string
		want []transcript.Turn
	}{
		{
			name: "several blocks in one event",
			line: `{"type":"assistant","message":{"content":[` +
				`{"type":"thinking","thinking":"Consider the handler."},` +
				`{"type":"text","text":"Checking."},` +
				`{"type":"tool_use","name":"Read","input":{"file_path":"a.go"}}]}}`,
			want: []transcript.Turn{
				{Role: transcript.RoleAssistant, Kind: transcript.KindThinking, Text: "Consider the handler."},
				{Role: transcript.RoleAssistant, Kind: transcript.KindText, Text: "Checking."},
				{Role: transcript.RoleAssistant, Kind: transcript.KindToolUse, Tool: "Read", Text: "a.go"},
			},
		},
		{
			name: "tool result as bare string",
			line: `{"type":"user","message":{"content":[{"type":"tool_result","content":"ok"}]}}`,
			want: []transcript.Turn{
				{Role: transcript.RoleUser, Kind: transcript.KindToolResult, Text: "ok"},
			},
		},
		{
			name: "tool result as blocks",
			line: `{"type":"user","message":{"content":[{"type":"tool_result","content":` +
				`[{"type":"text","text":"line one"},{"type":"text","text":"line two"}]}]}}`,
			want: []transcript.Turn{
				{Role: transcript.RoleUser, Kind: transcript.KindToolResult, Text: "line one\nline two"},
			},
		},
		{
			name: "failed tool result is marked",
			line: `{"type":"user","message":{"content":[{"type":"tool_result","is_error":true,"content":"boom"}]}}`,
			want: []transcript.Turn{
				{Role: transcript.RoleUser, Kind: transcript.KindToolResult, Text: "[error] boom"},
			},
		},
		{
			name: "tool input with no identifying key falls back to the object",
			line: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Odd","input":{"a":1}}]}}`,
			want: []transcript.Turn{
				{Role: transcript.RoleAssistant, Kind: transcript.KindToolUse, Tool: "Odd", Text: `{"a":1}`},
			},
		},
		{name: "empty text block is dropped",
			line: `{"type":"assistant","message":{"content":[{"type":"text","text":"  "}]}}`},
		{name: "not json", line: `plain log output`},
		{name: "unknown event type", line: `{"type":"stream_event","foo":1}`},
	}

	c := NewClaude()
	for _, tt := range tests {
		got := c.ScanTurns([]byte(tt.line))
		if len(got) != len(tt.want) {
			t.Errorf("%s: got %d turns, want %d: %+v", tt.name, len(got), len(tt.want), got)
			continue
		}
		for i := range tt.want {
			if got[i].Role != tt.want[i].Role || got[i].Kind != tt.want[i].Kind ||
				got[i].Tool != tt.want[i].Tool || got[i].Text != tt.want[i].Text {
				t.Errorf("%s: turn %d = %+v, want %+v", tt.name, i, got[i], tt.want[i])
			}
		}
	}
}

func TestFakeScanTurnsMatchesClaude(t *testing.T) {
	// The e2e replay depends on this: a fixture must transcribe like live output.
	fake, claude := scanAll(NewFake(), claudeStreamSuccess), scanAll(NewClaude(), claudeStreamSuccess)
	if len(fake) != len(claude) {
		t.Fatalf("fake produced %d turns, claude %d", len(fake), len(claude))
	}
	for i := range claude {
		if fake[i] != claude[i] {
			t.Errorf("turn %d: fake %+v, claude %+v", i, fake[i], claude[i])
		}
	}
}

func TestCodexScanTurns(t *testing.T) {
	tests := []struct {
		name string
		line string
		want []transcript.Turn
	}{
		{
			name: "thread banner",
			line: `{"type":"thread.started","thread_id":"th_1"}`,
			want: []transcript.Turn{
				{Role: transcript.RoleSystem, Kind: transcript.KindNotice, Text: "thread th_1 started"},
			},
		},
		{
			name: "agent message",
			line: `{"type":"item.completed","item":{"type":"agent_message","text":"Found it."}}`,
			want: []transcript.Turn{
				{Role: transcript.RoleAssistant, Kind: transcript.KindText, Text: "Found it."},
			},
		},
		{
			name: "reasoning",
			line: `{"type":"item.completed","item":{"type":"reasoning","text":"Weighing options."}}`,
			want: []transcript.Turn{
				{Role: transcript.RoleAssistant, Kind: transcript.KindThinking, Text: "Weighing options."},
			},
		},
		{
			name: "command splits into call and result",
			line: `{"type":"item.completed","item":{"type":"command_execution",` +
				`"command":"go build ./...","aggregated_output":"ok","exit_code":0}}`,
			want: []transcript.Turn{
				{Role: transcript.RoleAssistant, Kind: transcript.KindToolUse, Tool: "Bash", Text: "go build ./..."},
				{Role: transcript.RoleUser, Kind: transcript.KindToolResult, Text: "ok"},
			},
		},
		{
			name: "nonzero exit marks the result",
			line: `{"type":"item.completed","item":{"type":"command_execution",` +
				`"command":"go vet","aggregated_output":"bad","exit_code":2}}`,
			want: []transcript.Turn{
				{Role: transcript.RoleAssistant, Kind: transcript.KindToolUse, Tool: "Bash", Text: "go vet"},
				{Role: transcript.RoleUser, Kind: transcript.KindToolResult, Text: "[error] bad"},
			},
		},
		{
			name: "turn failure",
			line: `{"type":"turn.failed","error":{"message":"rate limited"}}`,
			want: []transcript.Turn{
				{Role: transcript.RoleSystem, Kind: transcript.KindNotice, Text: "turn failed: rate limited"},
			},
		},
		{name: "unmapped item type", line: `{"type":"item.completed","item":{"type":"mystery","text":"x"}}`},
		{name: "turn completed carries no turn", line: `{"type":"turn.completed","usage":{"output_tokens":5}}`},
	}

	c := NewCodex()
	for _, tt := range tests {
		got := c.ScanTurns([]byte(tt.line))
		if len(got) != len(tt.want) {
			t.Errorf("%s: got %d turns, want %d: %+v", tt.name, len(got), len(tt.want), got)
			continue
		}
		for i := range tt.want {
			if got[i].Role != tt.want[i].Role || got[i].Kind != tt.want[i].Kind ||
				got[i].Tool != tt.want[i].Tool || got[i].Text != tt.want[i].Text {
				t.Errorf("%s: turn %d = %+v, want %+v", tt.name, i, got[i], tt.want[i])
			}
		}
	}
}

func TestCopilotScanTurns(t *testing.T) {
	tests := []struct {
		name string
		line string
		want []transcript.Turn
	}{
		{
			name: "session banner",
			line: `{"type":"session.start","sessionId":"s1"}`,
			want: []transcript.Turn{
				{Role: transcript.RoleSystem, Kind: transcript.KindNotice, Text: "session s1 started"},
			},
		},
		{
			name: "assistant message",
			line: `{"type":"assistant.message","data":{"content":"Looking."}}`,
			want: []transcript.Turn{
				{Role: transcript.RoleAssistant, Kind: transcript.KindText, Text: "Looking."},
			},
		},
		{
			name: "session error",
			line: `{"type":"session.error","data":{"message":"denied"}}`,
			want: []transcript.Turn{
				{Role: transcript.RoleSystem, Kind: transcript.KindNotice, Text: "error: denied"},
			},
		},
		{name: "usage carries no turn", line: `{"type":"assistant.usage","data":{"outputTokens":5}}`},
	}

	c := NewCopilot()
	for _, tt := range tests {
		got := c.ScanTurns([]byte(tt.line))
		if len(got) != len(tt.want) {
			t.Errorf("%s: got %d turns, want %d: %+v", tt.name, len(got), len(tt.want), got)
			continue
		}
		for i := range tt.want {
			if got[i].Role != tt.want[i].Role || got[i].Kind != tt.want[i].Kind || got[i].Text != tt.want[i].Text {
				t.Errorf("%s: turn %d = %+v, want %+v", tt.name, i, got[i], tt.want[i])
			}
		}
	}
}

func TestEveryBuiltinHarnessScansTurns(t *testing.T) {
	// A harness that silently lacks the capability produces no transcript at
	// all, which is a hard failure to notice in production.
	for _, h := range All() {
		if _, ok := h.(TurnScanner); !ok {
			t.Errorf("harness %q does not implement TurnScanner", h.ID())
		}
	}
}
