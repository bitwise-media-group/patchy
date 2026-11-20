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
	for line := range strings.SplitSeq(stream, "\n") {
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
				{Role: transcript.RoleAssistant, Kind: transcript.KindToolUse, Tool: "Read", Path: "a.go", Text: "a.go"},
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
		{
			name: "tool_use carries its id",
			line: `{"type":"assistant","message":{"content":[` +
				`{"type":"tool_use","id":"toolu_1","name":"Read","input":{"file_path":"src/app.go"}}]}}`,
			want: []transcript.Turn{
				{Role: transcript.RoleAssistant, Kind: transcript.KindToolUse, Tool: "Read",
					ID: "toolu_1", Path: "src/app.go", Text: "src/app.go"},
			},
		},
		{
			name: "tool_result pairs back to the call",
			line: `{"type":"user","message":{"content":[` +
				`{"type":"tool_result","tool_use_id":"toolu_1","content":"     1\tpackage main"}]}}`,
			want: []transcript.Turn{
				{Role: transcript.RoleUser, Kind: transcript.KindToolResult, ID: "toolu_1", Text: "1\tpackage main"},
			},
		},
		{
			name: "edit keeps its diff",
			line: `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_2","name":"Edit",` +
				`"input":{"file_path":"a.go","old_string":"x := 1","new_string":"x := 2"}}]}}`,
			want: []transcript.Turn{
				{Role: transcript.RoleAssistant, Kind: transcript.KindToolUse, Tool: "Edit", ID: "toolu_2",
					Path: "a.go", Syntax: transcript.SyntaxDiff, Text: "-x := 1\n+x := 2"},
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
			if got[i] != tt.want[i] {
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
				{Role: transcript.RoleAssistant, Kind: transcript.KindToolUse, Tool: "Bash",
					Syntax: transcript.SyntaxShell, Text: "go build ./..."},
				{Role: transcript.RoleUser, Kind: transcript.KindToolResult, Text: "ok"},
			},
		},
		{
			name: "nonzero exit marks the result",
			line: `{"type":"item.completed","item":{"type":"command_execution",` +
				`"command":"go vet","aggregated_output":"bad","exit_code":2}}`,
			want: []transcript.Turn{
				{Role: transcript.RoleAssistant, Kind: transcript.KindToolUse, Tool: "Bash",
					Syntax: transcript.SyntaxShell, Text: "go vet"},
				{Role: transcript.RoleUser, Kind: transcript.KindToolResult, Text: "[error] bad"},
			},
		},
		{
			name: "item id pairs call and result",
			line: `{"type":"item.completed","item":{"id":"item_3","type":"command_execution",` +
				`"command":"ls","aggregated_output":"a.go","exit_code":0}}`,
			want: []transcript.Turn{
				{Role: transcript.RoleAssistant, Kind: transcript.KindToolUse, Tool: "Bash", ID: "item_3",
					Syntax: transcript.SyntaxShell, Text: "ls"},
				{Role: transcript.RoleUser, Kind: transcript.KindToolResult, ID: "item_3", Text: "a.go"},
			},
		},
		{
			name: "file change lists its files",
			line: `{"type":"item.completed","item":{"id":"item_4","type":"file_change","status":"completed",` +
				`"changes":[{"path":"a.go","kind":"update"},{"path":"b.go","kind":"add"}]}}`,
			want: []transcript.Turn{
				{Role: transcript.RoleAssistant, Kind: transcript.KindToolUse, Tool: "Edit", ID: "item_4",
					Text: "update a.go\nadd b.go"},
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
			if got[i] != tt.want[i] {
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

func TestRenderToolCall(t *testing.T) {
	tests := []struct {
		name       string
		tool       string
		input      string
		wantText   string
		wantPath   string
		wantSyntax transcript.Syntax
	}{
		{name: "edit is a diff", tool: "Edit",
			input:    `{"file_path":"/w/a.go","old_string":"a\nb\n","new_string":"a\nc\n"}`,
			wantText: "-a\n-b\n+a\n+c", wantPath: "/w/a.go", wantSyntax: transcript.SyntaxDiff},
		{name: "edit deleting text", tool: "Edit",
			input:    `{"file_path":"a.go","old_string":"gone","new_string":""}`,
			wantText: "-gone", wantPath: "a.go", wantSyntax: transcript.SyntaxDiff},
		{name: "multiedit joins hunks", tool: "MultiEdit",
			input:    `{"file_path":"a.go","edits":[{"old_string":"a","new_string":"b"},{"old_string":"c","new_string":"d"}]}`,
			wantText: "-a\n+b\n@@\n-c\n+d", wantPath: "a.go", wantSyntax: transcript.SyntaxDiff},
		{name: "write keeps its content", tool: "Write",
			input:    `{"file_path":"main.py","content":"print('hi')\n"}`,
			wantText: "print('hi')\n", wantPath: "main.py"},
		{name: "write of an empty file", tool: "Write",
			input: `{"file_path":"empty.txt","content":""}`, wantPath: "empty.txt"},
		{name: "read is exactly its path", tool: "Read",
			input:    `{"file_path":"src/app.ts","offset":10,"limit":20}`,
			wantText: "src/app.ts", wantPath: "src/app.ts"},
		{name: "bash is shell", tool: "Bash",
			input:    `{"command":"go test ./...","description":"run tests"}`,
			wantText: "go test ./...", wantSyntax: transcript.SyntaxShell},
		{name: "other tools project one key", tool: "Grep",
			input: `{"pattern":"TODO","glob":"*.go"}`, wantText: "TODO"},
		{name: "edit without a path falls back", tool: "Edit",
			input: `{"old_string":"a","new_string":"b"}`, wantText: `{"new_string":"b","old_string":"a"}`},
		{name: "malformed input", tool: "Edit", input: `not json`, wantText: "not json"},
		{name: "no input", tool: "Bash"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, path, syntax := renderToolCall(tt.tool, []byte(tt.input))
			if text != tt.wantText || path != tt.wantPath || syntax != tt.wantSyntax {
				t.Errorf("renderToolCall = (%q, %q, %q), want (%q, %q, %q)",
					text, path, syntax, tt.wantText, tt.wantPath, tt.wantSyntax)
			}
		})
	}
}
