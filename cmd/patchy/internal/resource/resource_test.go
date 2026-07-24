// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package resource

import (
	"strings"
	"testing"
)

func TestLookup(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"finding", "finding"},
		{"findings", "finding"},
		{"fnd", "finding"},
		{"FINDINGS", "finding"},
		{"  findings  ", "finding"},
		{"inv", "investigation"},
		{"investigations", "investigation"},
		{"rem", "remediation"},
		{"fr", "findingrollup"},
		{"rollups", "findingrollup"},
		{"repo", "repository"},
		{"repositories", "repository"},
		{"forges", "forge"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			k, err := Lookup(tc.in)
			if err != nil {
				t.Fatalf("Lookup(%q): %v", tc.in, err)
			}
			if k.Singular != tc.want {
				t.Errorf("Lookup(%q) = %q, want %q", tc.in, k.Singular, tc.want)
			}
		})
	}
}

func TestLookupUnknown(t *testing.T) {
	_, err := Lookup("widget")
	if err == nil {
		t.Fatal("Lookup accepted an unknown noun")
	}
	// The error is the discovery path for someone who guessed wrong, so it has
	// to list the alternatives.
	if !strings.Contains(err.Error(), "finding") {
		t.Errorf("error = %q, want it to list the known nouns", err)
	}
}

// TestKindsAreWellFormed catches a half-added noun: one missing its list type
// or plural fails at first use rather than here.
func TestKindsAreWellFormed(t *testing.T) {
	seen := map[string]string{}
	for _, k := range Kinds {
		if k.Singular == "" || k.Plural == "" || k.New == nil || k.NewList == nil {
			t.Errorf("kind %+v is incomplete", k)
			continue
		}
		if k.New() == nil || k.NewList() == nil {
			t.Errorf("kind %s constructs nil", k.Singular)
		}
		// Every spelling must resolve to exactly one kind, or `patchy get X`
		// silently depends on declaration order.
		for _, spelling := range append([]string{k.Singular, k.Plural}, k.Aliases...) {
			if owner, dup := seen[spelling]; dup {
				t.Errorf("spelling %q is claimed by both %s and %s", spelling, owner, k.Singular)
			}
			seen[spelling] = k.Singular
		}
	}
}

// TestSpellingsResolve keeps completion honest: everything it offers must work.
func TestSpellingsResolve(t *testing.T) {
	for _, s := range Spellings() {
		if _, err := Lookup(s); err != nil {
			t.Errorf("completion offers %q but Lookup rejects it: %v", s, err)
		}
	}
}

func TestRun(t *testing.T) {
	for _, tc := range []struct {
		noun string
		want bool
	}{
		{"investigation", true},
		{"remediation", true},
		{"finding", false},
		{"repository", false},
	} {
		k, err := Lookup(tc.noun)
		if err != nil {
			t.Fatalf("Lookup(%q): %v", tc.noun, err)
		}
		if got := k.Run(); got != tc.want {
			t.Errorf("%s.Run() = %v, want %v", tc.noun, got, tc.want)
		}
	}
}
