// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package semverpick

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestReleases(t *testing.T) {
	in := []string{
		"0.166.0", "latest", "0.168.0", "sha256-abc.sig", "v1.2.3-rc.1",
		"0.169.0", "v0.9.0", "1.0.0+meta", "main",
	}
	got := Releases(in)
	want := []string{"0.169.0", "0.168.0", "0.166.0", "v0.9.0"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("Releases = %v, want %v", got, want)
	}
}

func TestDefaultConstraint(t *testing.T) {
	for pin, want := range map[string]string{
		"v2.44.0": ">=2.44.0 <3.0.0",
		"0.21.0":  ">=0.21.0 <1.0.0",
		"2.336.0": ">=2.336.0 <3.0.0",
	} {
		got, err := DefaultConstraint(pin)
		if err != nil {
			t.Errorf("DefaultConstraint(%q): %v", pin, err)
		}
		if got != want {
			t.Errorf("DefaultConstraint(%q) = %q, want %q", pin, got, want)
		}
	}
	if _, err := DefaultConstraint("latest"); err == nil {
		t.Error("DefaultConstraint(latest): want error")
	}
}

func TestSatisfies(t *testing.T) {
	tests := []struct {
		v, c    string
		want    bool
		wantErr bool
	}{
		{v: "0.165.0", c: ">=0.130.0 <1.0.0", want: true},
		{v: "1.0.0", c: ">=0.130.0 <1.0.0", want: false},
		{v: "v1.21.0", c: ">=1.18.0 <2.0.0", want: true},
		{v: "2.321.0", c: ">=2.320.0 <3.0.0", want: true},
		{v: "1.5.0", c: ">=1.0.0 <2.0.0 !=1.5.0", want: false},
		{v: "1.6.0", c: ">=1.0.0 <2.0.0 !=1.5.0", want: true},
		{v: "1.2.3", c: "", want: true},
		{v: "not-a-version", c: "", wantErr: true},
		{v: "1.2.3", c: "gibberish", wantErr: true},
	}
	for _, tt := range tests {
		got, err := Satisfies(tt.v, tt.c)
		if tt.wantErr {
			if err == nil {
				t.Errorf("Satisfies(%q, %q): want error", tt.v, tt.c)
			}
			continue
		}
		if err != nil {
			t.Errorf("Satisfies(%q, %q): %v", tt.v, tt.c, err)
			continue
		}
		if got != tt.want {
			t.Errorf("Satisfies(%q, %q) = %v, want %v", tt.v, tt.c, got, tt.want)
		}
	}
}

func TestNewest(t *testing.T) {
	tests := []struct {
		name       string
		current    string
		candidates []string
		constraint string
		want       string
		wantOK     bool
	}{
		{
			name:       "newer available",
			current:    "0.165.0",
			candidates: []string{"0.164.0", "0.166.0", "0.169.0", "1.2.0", "latest"},
			constraint: ">=0.130.0 <1.0.0",
			want:       "0.169.0",
			wantOK:     true,
		},
		{
			name:       "already current",
			current:    "0.169.0",
			candidates: []string{"0.164.0", "0.166.0", "0.169.0"},
			constraint: ">=0.130.0 <1.0.0",
		},
		{
			name:       "constraint holds back",
			current:    "1.21.0",
			candidates: []string{"1.21.1", "2.0.0"},
			constraint: ">=1.15.0 <2.0.0",
			want:       "1.21.1",
			wantOK:     true,
		},
		{
			name:       "v-prefixed pins and tags",
			current:    "v1.21.0",
			candidates: []string{"v1.21.1", "v1.22.0"},
			constraint: ">=1.18.0 <2.0.0",
			want:       "v1.22.0",
			wantOK:     true,
		},
		{
			name:       "prereleases ignored",
			current:    "1.0.0",
			candidates: []string{"1.1.0-rc.1", "1.1.0-alpha"},
		},
		{
			name:       "no constraint",
			current:    "1.0.0",
			candidates: []string{"2.0.0", "3.0.0"},
			want:       "3.0.0",
			wantOK:     true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok, err := Newest(tt.current, tt.candidates, tt.constraint)
			if err != nil {
				t.Fatalf("Newest: %v", err)
			}
			if ok != tt.wantOK || got != tt.want {
				t.Errorf("Newest = (%q, %v), want (%q, %v)", got, ok, tt.want, tt.wantOK)
			}
		})
	}

	if _, _, err := Newest("junk", nil, ""); err == nil {
		t.Error("Newest with unparseable current: want error")
	}
}

func TestCooldownPick(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	published := map[string]time.Time{
		"2.322.0": now.Add(-1 * day),  // inside a 3d cooldown
		"2.321.0": now.Add(-10 * day), // clear
		"2.320.0": now.Add(-40 * day), // clear
	}
	created := func(tag string) (time.Time, bool, error) {
		if tag == "6.6.6" {
			return time.Time{}, false, fmt.Errorf("registry exploded")
		}
		ts, ok := published[tag]
		return ts, ok, nil
	}

	t.Run("skips inside cooldown", func(t *testing.T) {
		var skips []string
		got, err := CooldownPick(
			[]string{"2.320.0", "2.321.0", "2.322.0", "latest"},
			">=2.320.0 <3.0.0", 3*day, now, created,
			func(tag, reason string) { skips = append(skips, tag+": "+reason) },
		)
		if err != nil {
			t.Fatalf("CooldownPick: %v", err)
		}
		if got != "2.321.0" {
			t.Errorf("picked %q, want 2.321.0", got)
		}
		if len(skips) != 1 || !strings.HasPrefix(skips[0], "2.322.0:") {
			t.Errorf("skips = %v", skips)
		}
	})

	t.Run("zero cooldown picks newest", func(t *testing.T) {
		got, err := CooldownPick([]string{"2.320.0", "2.321.0", "2.322.0"}, "", 0, now, created, nil)
		if err != nil {
			t.Fatalf("CooldownPick: %v", err)
		}
		if got != "2.322.0" {
			t.Errorf("picked %q, want 2.322.0", got)
		}
	})

	t.Run("missing timestamp skipped", func(t *testing.T) {
		got, err := CooldownPick([]string{"2.321.0", "9.9.9"}, "", 3*day, now, created, nil)
		if err != nil {
			t.Fatalf("CooldownPick: %v", err)
		}
		if got != "2.321.0" {
			t.Errorf("picked %q, want 2.321.0", got)
		}
	})

	t.Run("created lookup failure propagates", func(t *testing.T) {
		if _, err := CooldownPick([]string{"6.6.6"}, "", 3*day, now, created, nil); err == nil {
			t.Error("want created-lookup error")
		}
	})

	t.Run("nothing clears", func(t *testing.T) {
		if _, err := CooldownPick([]string{"2.322.0"}, "", 3*day, now, created, nil); err == nil {
			t.Error("want error when nothing clears the cooldown")
		}
	})

	t.Run("candidate cap", func(t *testing.T) {
		tags := make([]string, 0, 40)
		for i := range 40 {
			tags = append(tags, fmt.Sprintf("1.%d.0", i))
		}
		fresh := func(string) (time.Time, bool, error) { return now, true, nil }
		_, err := CooldownPick(tags, "", 3*day, now, fresh, nil)
		if err == nil || !strings.Contains(err.Error(), "newest 25") {
			t.Errorf("want candidate-cap error, got %v", err)
		}
	})
}
