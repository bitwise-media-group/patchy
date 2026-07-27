// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package transcript

import (
	"strings"
	"testing"
)

// collect returns a recorder writing into the returned slice pointer.
func collect(limits Limits, secrets []string) (*Recorder, *[]Turn) {
	var got []Turn
	r := NewRecorder(limits, secrets, func(t Turn) { got = append(got, t) })
	return r, &got
}

func TestRecorderSequencesAndStamps(t *testing.T) {
	r, got := collect(Limits{}, nil)
	r.RecordAll([]Turn{
		{Role: RoleAssistant, Kind: KindText, Text: "one"},
		{Role: RoleAssistant, Kind: KindToolUse, Tool: "Bash", Text: "two"},
	})
	if len(*got) != 2 {
		t.Fatalf("emitted %d turns, want 2", len(*got))
	}
	for i, turn := range *got {
		if turn.Seq != i+1 {
			t.Errorf("turn %d Seq = %d, want %d", i, turn.Seq, i+1)
		}
		if turn.At == "" {
			t.Errorf("turn %d has no timestamp", i)
		}
	}
	if n, truncated := r.Stats(); n != 2 || truncated {
		t.Errorf("Stats = (%d, %v), want (2, false)", n, truncated)
	}
}

func TestRecorderDropsEmptyKind(t *testing.T) {
	r, got := collect(Limits{}, nil)
	r.Record(Turn{Role: RoleAssistant, Text: "no kind"})
	if len(*got) != 0 {
		t.Errorf("emitted %d turns, want 0", len(*got))
	}
}

func TestRecorderRedactsSecrets(t *testing.T) {
	const key = "sk-ant-supersecretvalue"
	r, got := collect(Limits{}, []string{key, "short"})
	r.Record(Turn{Role: RoleUser, Kind: KindToolResult, Text: "ANTHROPIC_API_KEY=" + key})
	if len(*got) != 1 {
		t.Fatalf("emitted %d turns, want 1", len(*got))
	}
	text := (*got)[0].Text
	if strings.Contains(text, key) {
		t.Errorf("Text = %q, still carries the credential", text)
	}
	if !strings.Contains(text, Redacted) {
		t.Errorf("Text = %q, want %s", text, Redacted)
	}
}

func TestRecorderStripsANSI(t *testing.T) {
	r, got := collect(Limits{}, nil)
	r.Record(Turn{Role: RoleUser, Kind: KindToolResult, Text: "\x1b[31mFAIL\x1b[0m ok"})
	if want := "FAIL ok"; (*got)[0].Text != want {
		t.Errorf("Text = %q, want %q", (*got)[0].Text, want)
	}
}

func TestRecorderCapsTurnBytes(t *testing.T) {
	r, got := collect(Limits{MaxTurnBytes: 4}, nil)
	r.Record(Turn{Role: RoleAssistant, Kind: KindText, Text: "abcdefgh"})
	if (*got)[0].Text != "abcd" {
		t.Errorf("Text = %q, want %q", (*got)[0].Text, "abcd")
	}
	if !(*got)[0].Truncated {
		t.Error("Truncated = false, want true")
	}
	if _, truncated := r.Stats(); !truncated {
		t.Error("Stats truncated = false, want true")
	}
}

func TestRecorderStopsAtTurnCap(t *testing.T) {
	r, got := collect(Limits{MaxTurns: 2}, nil)
	for range 5 {
		r.Record(Turn{Role: RoleAssistant, Kind: KindText, Text: "x"})
	}
	// Two admitted turns plus the closing notice.
	if len(*got) != 3 {
		t.Fatalf("emitted %d turns, want 3", len(*got))
	}
	last := (*got)[2]
	if last.Kind != KindNotice || !strings.Contains(last.Text, "turn cap") {
		t.Errorf("last turn = %+v, want a turn-cap notice", last)
	}
	if _, truncated := r.Stats(); !truncated {
		t.Error("Stats truncated = false, want true")
	}
}

func TestRecorderStopsAtByteCap(t *testing.T) {
	r, got := collect(Limits{MaxTotalBytes: 8}, nil)
	for range 5 {
		r.Record(Turn{Role: RoleAssistant, Kind: KindText, Text: "abcde"})
	}
	last := (*got)[len(*got)-1]
	if last.Kind != KindNotice || !strings.Contains(last.Text, "byte cap") {
		t.Errorf("last turn = %+v, want a byte-cap notice", last)
	}
	// Nothing is admitted after the notice.
	before := len(*got)
	r.Record(Turn{Role: RoleAssistant, Kind: KindText, Text: "more"})
	if len(*got) != before {
		t.Errorf("emitted %d turns after stop, want %d", len(*got), before)
	}
}

func TestRecorderNotice(t *testing.T) {
	r, got := collect(Limits{}, nil)
	r.Notice("output token budget exceeded (%d > %d)", 10, 5)
	if len(*got) != 1 {
		t.Fatalf("emitted %d turns, want 1", len(*got))
	}
	if (*got)[0].Kind != KindNotice || (*got)[0].Role != RoleSystem {
		t.Errorf("turn = %+v, want a system notice", (*got)[0])
	}
	if !strings.Contains((*got)[0].Text, "10 > 5") {
		t.Errorf("Text = %q, want the formatted args", (*got)[0].Text)
	}
}
