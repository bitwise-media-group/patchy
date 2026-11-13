// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package mirror

import (
	"testing"

	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
)

// TestMatchVerifyRule pins rule selection under path globs: first match
// wins, '*' stays within one segment, and "**" is the catch-all.
func TestMatchVerifyRule(t *testing.T) {
	rules := []spec.ImageVerifyRule{
		{Match: "ghcr.io/acme/*", VerifyRule: spec.VerifyRule{Provider: "cosign-keyless"}},
		{Match: "ghcr.io/acme/**", VerifyRule: spec.VerifyRule{Provider: "cosign-key"}},
		{Match: "**", VerifyRule: spec.VerifyRule{Provider: "none"}},
	}
	tests := []struct {
		source, want string
	}{
		{"ghcr.io/acme/app:v1", "ghcr.io/acme/*"},
		{"ghcr.io/acme/team/app:v1", "ghcr.io/acme/**"},
		{"ghcr.io/acme-evil/app:v1", "**"},
		{"docker.io/library/nginx:1.27", "**"},
	}
	for _, tt := range tests {
		r, ok := matchVerifyRule(rules, tt.source)
		if !ok || r.Match != tt.want {
			t.Errorf("matchVerifyRule(%s) = %q (%v), want %q", tt.source, r.Match, ok, tt.want)
		}
	}
	if _, ok := matchVerifyRule(rules[:1], "docker.io/library/nginx:1.27"); ok {
		t.Error("an image no rule covers matched")
	}
}
