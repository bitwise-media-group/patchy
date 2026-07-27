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
