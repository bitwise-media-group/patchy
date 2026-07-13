// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package harness

import "testing"

func TestByID(t *testing.T) {
	tests := []struct {
		id   string
		ok   bool
		name string
	}{
		{"claude", true, "Claude Code"},
		{"fake", true, "Fake"},
		{"codex", false, ""},
		{"", false, ""},
	}
	for _, tt := range tests {
		h, ok := ByID(tt.id)
		if ok != tt.ok {
			t.Errorf("ByID(%q) ok = %v, want %v", tt.id, ok, tt.ok)
			continue
		}
		if ok && (h.ID() != tt.id || h.Name() != tt.name) {
			t.Errorf("ByID(%q) = (%q, %q), want (%q, %q)", tt.id, h.ID(), h.Name(), tt.id, tt.name)
		}
	}
}

func TestAvailableFake(t *testing.T) {
	// The fake harness runs through cat, which every unix PATH carries.
	if path, ok := Available(NewFake()); !ok || path == "" {
		t.Errorf("Available(fake) = (%q, %v), want cat found on PATH", path, ok)
	}
}

func TestHarnessesImplementUsageScanner(t *testing.T) {
	for _, h := range All() {
		if _, ok := h.(UsageScanner); !ok {
			t.Errorf("harness %q does not implement UsageScanner", h.ID())
		}
	}
}
