// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package transcript

import (
	"strings"
	"testing"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	want := Turn{Seq: 3, Role: RoleAssistant, Kind: KindToolUse, Tool: "Bash", Text: "go test ./..."}
	line, err := Encode(want)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !strings.HasPrefix(line, Prefix) {
		t.Fatalf("Encode = %q, want %s prefix", line, Prefix)
	}
	got, ok := Decode([]byte(line))
	if !ok {
		t.Fatalf("Decode(%q) = not ok", line)
	}
	if got.Seq != want.Seq || got.Tool != want.Tool || got.Text != want.Text || got.Kind != want.Kind {
		t.Errorf("Decode = %+v, want %+v", got, want)
	}
	if got.V != Version {
		t.Errorf("V = %d, want %d", got.V, Version)
	}
}

func TestDecodeRejects(t *testing.T) {
	tests := []struct {
		name string
		line string
	}{
		{"no prefix", `{"v":1,"kind":"text"}`},
		{"envelope line", `PATCHY-EVENT: {"v":4,"type":"investigation"}`},
		{"bad json", Prefix + `{not json`},
		{"wrong version", Prefix + `{"v":99,"kind":"text"}`},
		{"no kind", Prefix + `{"v":1,"seq":1}`},
		{"plain log", "time=2026-07-27 level=INFO msg=running"},
	}
	for _, tt := range tests {
		if _, ok := Decode([]byte(tt.line)); ok {
			t.Errorf("%s: Decode(%q) = ok, want not ok", tt.name, tt.line)
		}
	}
}

func TestDecodeFindsWrappedPrefix(t *testing.T) {
	// Kubernetes may prepend a timestamp to the line.
	line := `2026-07-27T10:00:00Z ` + Prefix + `{"v":1,"seq":1,"kind":"text","text":"hi"}`
	got, ok := Decode([]byte(line))
	if !ok {
		t.Fatalf("Decode(%q) = not ok", line)
	}
	if got.Text != "hi" {
		t.Errorf("Text = %q, want %q", got.Text, "hi")
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		limit     int
		want      string
		truncated bool
	}{
		{"under limit", "abc", 10, "abc", false},
		{"at limit", "abcde", 5, "abcde", false},
		{"over limit", "abcdefgh", 5, "abcde", true},
		{"rune boundary", "aaéé", 3, "aa", true},
	}
	for _, tt := range tests {
		got, cut := Truncate(tt.in, tt.limit)
		if got != tt.want || cut != tt.truncated {
			t.Errorf("%s: Truncate(%q, %d) = (%q, %v), want (%q, %v)",
				tt.name, tt.in, tt.limit, got, cut, tt.want, tt.truncated)
		}
	}
}

func TestEncodeDecodeCarriesToolFields(t *testing.T) {
	want := Turn{Seq: 1, Role: RoleAssistant, Kind: KindToolUse, Tool: "Edit",
		ID: "toolu_1", Path: "main.go", Syntax: SyntaxDiff, Text: "-a\n+b"}
	line, err := Encode(want)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, ok := Decode([]byte(line))
	if !ok {
		t.Fatal("Decode ok = false")
	}
	if got.ID != want.ID || got.Path != want.Path || got.Syntax != want.Syntax {
		t.Errorf("Decode = %+v, want ID/Path/Syntax of %+v", got, want)
	}
}

func TestDecodeOldRecordWithoutToolFields(t *testing.T) {
	// A record written before ID/Path/Syntax existed still decodes.
	got, ok := Decode([]byte(Prefix + `{"v":1,"seq":2,"role":"assistant","kind":"tool_use","tool":"Bash","text":"ls"}`))
	if !ok || got.Seq != 2 || got.ID != "" || got.Path != "" || got.Syntax != "" {
		t.Errorf("Decode = (%+v, %v)", got, ok)
	}
}

// numbered returns n text turns with Seq 1..n, each text bytes long.
func numbered(n, text int) []Turn {
	turns := make([]Turn, n)
	for i := range turns {
		turns[i] = Turn{Seq: i + 1, Role: RoleAssistant, Kind: KindText, Text: strings.Repeat("x", text)}
	}
	return turns
}

func TestCompact(t *testing.T) {
	tests := []struct {
		name       string
		turns      []Turn
		limits     Limits
		wantLen    int
		wantHead   int // turns before the notice
		wantElided string
		wantMaxLen int // longest Text kept
	}{
		{name: "within bounds", turns: numbered(10, 5), limits: Limits{}, wantLen: 10, wantMaxLen: 5},
		{name: "turn cap", turns: numbered(20, 1), limits: Limits{MaxTurns: 5},
			wantLen: 5, wantHead: 2, wantElided: "16 turns elided", wantMaxLen: 1},
		{name: "byte cap", turns: numbered(10, 10), limits: Limits{MaxTotalBytes: 45},
			wantLen: 5, wantHead: 2, wantElided: "6 turns elided", wantMaxLen: 10},
		{name: "per-turn cap", turns: numbered(3, 100), limits: Limits{MaxTurnBytes: 7}, wantLen: 3, wantMaxLen: 7},
		{name: "default store bounds", turns: numbered(2000, 10), limits: Limits{},
			wantLen: DefaultStoreMaxTurns, wantHead: 250, wantElided: "1501 turns elided", wantMaxLen: 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := append([]Turn(nil), tt.turns...)
			got := Compact(tt.turns, tt.limits)
			if len(got) != tt.wantLen {
				t.Fatalf("len = %d, want %d", len(got), tt.wantLen)
			}
			for i := range in {
				if in[i] != tt.turns[i] {
					t.Fatal("Compact modified its input")
				}
			}
			for _, turn := range got {
				if len(turn.Text) > tt.wantMaxLen && turn.Kind != KindNotice {
					t.Errorf("turn %d text %d bytes, want <= %d", turn.Seq, len(turn.Text), tt.wantMaxLen)
				}
			}
			if tt.wantElided == "" {
				if Truncated(got) != (tt.limits.MaxTurnBytes > 0) {
					t.Errorf("Truncated = %v", Truncated(got))
				}
				return
			}
			notice := got[tt.wantHead]
			if notice.Kind != KindNotice || !notice.Truncated || !strings.Contains(notice.Text, tt.wantElided) {
				t.Errorf("notice = %+v, want %q", notice, tt.wantElided)
			}
			// The head starts the run and the tail ends it.
			if got[0].Seq != 1 || got[len(got)-1].Seq != tt.turns[len(tt.turns)-1].Seq {
				t.Errorf("first/last Seq = %d/%d", got[0].Seq, got[len(got)-1].Seq)
			}
			// Seq stays strictly increasing, so a resumed stream can skip by it.
			for i := 1; i < len(got); i++ {
				if got[i].Seq <= got[i-1].Seq {
					t.Errorf("Seq not increasing at %d: %d after %d", i, got[i].Seq, got[i-1].Seq)
				}
			}
		})
	}
}
