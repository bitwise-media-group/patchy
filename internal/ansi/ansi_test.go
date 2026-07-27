// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package ansi

import "testing"

func TestStrip(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"plain", "hello\n", "hello\n"},
		{"csi color", "\x1b[31mred\x1b[0m", "red"},
		{"csi cursor", "a\x1b[2Kb", "ab"},
		{"osc bel", "\x1b]0;title\x07text", "text"},
		{"osc st", "\x1b]8;;http://x\x1b\\link", "link"},
		{"charset", "\x1b(Bok", "ok"},
		{"trailing esc", "end\x1b", "end"},
	}
	for _, tt := range tests {
		if got := string(Strip([]byte(tt.in))); got != tt.want {
			t.Errorf("%s: Strip(%q) = %q, want %q", tt.name, tt.in, got, tt.want)
		}
	}
}
