// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package expand

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

// env is a map-backed Lookup.
func env(m map[string]string) Lookup {
	return func(name string) (string, bool) {
		v, ok := m[name]
		return v, ok
	}
}

var testEnv = env(map[string]string{
	"VERSION":    "1.4.0",
	"CHANNEL":    "edge",
	"GITHUB_SHA": "0123456789abcdef0123456789abcdef01234567",
	"EMPTY":      "",
	"UNICODE":    "héllo",
})

func TestExpand(t *testing.T) {
	tests := []struct {
		in, want, wantErr string
	}{
		{in: "plain", want: "plain"},
		{in: "", want: ""},
		{in: "${VERSION}", want: "1.4.0"},
		{in: "v${VERSION}-${CHANNEL}", want: "v1.4.0-edge"},
		{in: "sha-${GITHUB_SHA}", want: "sha-0123456789abcdef0123456789abcdef01234567"},
		{in: "g${GITHUB_SHA:0:7}", want: "g0123456"},
		{in: "${GITHUB_SHA:38}", want: "67"},
		{in: "${GITHUB_SHA:40:5}", want: ""},
		{in: "${GITHUB_SHA:99:5}", want: ""},
		{in: "${GITHUB_SHA:2:0}", want: ""},
		{in: "${UNICODE:1:3}", want: "éll"},
		{in: "${MISSING:-fallback}", want: "fallback"},
		{in: "${EMPTY:-fallback}", want: "fallback"},
		{in: "${CHANNEL:-fallback}", want: "edge"},
		{in: "${MISSING:-}", want: ""},
		{in: "${MISSING:-${CHANNEL}-x}", want: "edge-x"},
		{in: "${MISSING:-a}b}", want: "ab}"},
		{in: "$$", want: "$"},
		{in: "$${VERSION}", want: "${VERSION}"},
		{in: "${MISSING:-$$}", want: "$"},
		{in: "${EMPTY}", want: ""},
		{in: "${MISSING}", wantErr: "${MISSING} is not set"},
		{in: "${MISSING:0:7}", wantErr: "${MISSING} is not set"},
		{in: "$VERSION", wantErr: "bare $"},
		{in: "trailing$", wantErr: "trailing $"},
		{in: "${VERSION", wantErr: "unterminated"},
		{in: "${}", wantErr: "expected a variable name"},
		{in: "${1X}", wantErr: "expected a variable name"},
		{in: "${VERSION:x}", wantErr: "offset"},
		{in: "${VERSION:0:-1}", wantErr: "length"},
		{in: "${VERSION/a/b}", wantErr: "unsupported syntax"},
		{in: "${MISSING:-${ALSO_MISSING}}", wantErr: "${ALSO_MISSING} is not set"},
	}
	for _, tt := range tests {
		got, err := Expand(tt.in, testEnv)
		if tt.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Expand(%q) = %q, %v; want error containing %q", tt.in, got, err, tt.wantErr)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("Expand(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
	}
}

// TestExpandEdgeScheme pins the terraform-aws-eks-flux tag scheme the
// build tags must be able to express.
func TestExpandEdgeScheme(t *testing.T) {
	ts, err := Timestamp(testEnv, time.Date(2026, 11, 10, 9, 8, 7, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Expand("${VERSION}-edge.${PATCHY_TIMESTAMP}.g${GITHUB_SHA:0:7}", WithBuiltins(testEnv, ts))
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`^[0-9]+[.][0-9]+[.][0-9]+-edge[.][0-9]{14}[.]g[0-9a-f]{7,40}$`)
	if !re.MatchString(got) || got != "1.4.0-edge.20261110090807.g0123456" {
		t.Errorf("edge tag = %q", got)
	}
}

func TestTimestamp(t *testing.T) {
	now := time.Date(2026, 11, 10, 9, 8, 7, 0, time.FixedZone("x", 3600))
	tests := []struct {
		name    string
		env     map[string]string
		want    string
		wantErr string
	}{
		{name: "now in UTC", want: "20261110080807"},
		{name: "SOURCE_DATE_EPOCH", env: map[string]string{"SOURCE_DATE_EPOCH": "1700000000"}, want: "20231114221320"},
		{
			name: "explicit PATCHY_TIMESTAMP wins",
			env:  map[string]string{"SOURCE_DATE_EPOCH": "1700000000", "PATCHY_TIMESTAMP": "20250101000000"},
			want: "20250101000000",
		},
		{name: "bad epoch", env: map[string]string{"SOURCE_DATE_EPOCH": "yesterday"}, wantErr: "SOURCE_DATE_EPOCH"},
		{name: "bad explicit", env: map[string]string{"PATCHY_TIMESTAMP": "2025"}, wantErr: "PATCHY_TIMESTAMP"},
	}
	for _, tt := range tests {
		got, err := Timestamp(env(tt.env), now)
		if tt.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("%s: Timestamp = %q, %v", tt.name, got, err)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("%s: Timestamp = %q, %v; want %q", tt.name, got, err, tt.want)
		}
	}
}

// FuzzExpand pins three properties: Expand never panics, a template with
// no $ is the identity, and escaping every $ as $$ round-trips any string.
func FuzzExpand(f *testing.F) {
	for _, seed := range []string{
		"", "plain", "${VERSION}", "${GITHUB_SHA:0:7}", "${X:-${Y:-z}}", "$$", "${", "$", "${A:1:", "}{$",
		"${VERSION}-edge.${PATCHY_TIMESTAMP}.g${GITHUB_SHA:0:7}",
	} {
		f.Add(seed)
	}
	lookup := WithBuiltins(testEnv, "20261110090807")
	f.Fuzz(func(t *testing.T, s string) {
		out, err := Expand(s, lookup)
		if !strings.Contains(s, "$") && (err != nil || out != s) {
			t.Fatalf("Expand(%q) = %q, %v; want the identity", s, out, err)
		}
		escaped := strings.ReplaceAll(s, "$", "$$")
		if got, err := Expand(escaped, lookup); err != nil || got != s {
			t.Fatalf("Expand(escape(%q)) = %q, %v; want the original", s, got, err)
		}
	})
}
