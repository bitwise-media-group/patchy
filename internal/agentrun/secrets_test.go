// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package agentrun

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/bitwise-media-group/patchy/internal/envelope"
	"github.com/bitwise-media-group/patchy/internal/transcript"
	"github.com/bitwise-media-group/patchy/pkg/source"
)

// fixtureSecret is the literal committed to the fixture repository. It is
// obviously fake and not token-shaped.
const fixtureSecret = "patchy-fixture-not-a-credential-0042"

// secretWorkspace is newWorkspace plus a config file carrying the secret at
// line 2, columns 10..45, and the secrets.json handoff locating it.
func secretWorkspace(t *testing.T) string {
	t.Helper()
	ws := newWorkspace(t)
	cfg := "service: billing\napi_key: " + fixtureSecret + "\n"
	if err := os.WriteFile(filepath.Join(ws, "repo", "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	start := len("api_key: ") + 1
	handoff := `[{"path":"config.yaml","start_line":2,"end_line":2,"start_column":` +
		strconv.Itoa(start) + `,"end_column":` + strconv.Itoa(start+len(fixtureSecret)) + `}]`
	if err := os.WriteFile(filepath.Join(ws, "input", "secrets.json"), []byte(handoff), 0o644); err != nil {
		t.Fatal(err)
	}
	return ws
}

// The agent echoes the secret in a tool result, the report and a summary;
// none of it may reach stdout, and the prompt must be the secret variant.
func TestSecretScrubbedFromTranscriptAndReport(t *testing.T) {
	ws := secretWorkspace(t)
	leakyReport := strings.Replace(goodInvestigation, "summary: full table read",
		`summary: "grants billing via `+fixtureSecret+`"`, 1) + "\nThe key is " + fixtureSecret + ".\n"
	var out bytes.Buffer
	fx := &fakeExec{steps: []step{
		{ws: ws, stdout: streamSuccess, budgetLines: []string{
			`{"type":"user","message":{"content":[{"type":"tool_result",` +
				`"content":"api_key: ` + fixtureSecret + `"}]}}`,
		}, writes: map[string]string{"reports/investigation.md": leakyReport}},
	}}
	if err := New(newConfig(t, ws, &out), fx).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if strings.Contains(out.String(), fixtureSecret) {
		t.Errorf("stdout carries the secret:\n%s", out.String())
	}
	got := turns(t, out.String())
	if len(got) != 1 || !strings.Contains(got[0].Text, transcript.Redacted) {
		t.Errorf("turns = %+v, want the value redacted", got)
	}
	evs := events(t, out.String())
	if len(evs) != 1 || evs[0].Investigation == nil {
		t.Fatalf("events = %+v, want one investigation", evs)
	}
	inv := evs[0].Investigation
	if inv.Outcome != envelope.OutcomeOK {
		t.Fatalf("outcome = %q (%s)", inv.Outcome, inv.Detail)
	}
	if !strings.Contains(inv.ReportMarkdown, "The key is "+transcript.Redacted) {
		t.Errorf("report = %q, want the secret redacted", inv.ReportMarkdown)
	}
	if !strings.Contains(inv.Impact.Summary, transcript.Redacted) {
		t.Errorf("impact summary = %q, want the secret redacted", inv.Impact.Summary)
	}
}

// The handoff switches the run into secret mode (the prompts' secret
// variants, pinned by the templates goldens) and registers the literal;
// without it the run is an ordinary finding.
func TestSecretHandoffSetsSecretMode(t *testing.T) {
	for _, tt := range []struct {
		name   string
		ws     func(*testing.T) string
		secret bool
	}{
		{"handoff present", secretWorkspace, true},
		{"no handoff", newWorkspace, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a := New(newConfig(t, tt.ws(t), &bytes.Buffer{}), &fakeExec{})
			if err := a.prepare(); err != nil {
				t.Fatalf("prepare() error = %v", err)
			}
			if a.secret != tt.secret {
				t.Errorf("secret = %v, want %v", a.secret, tt.secret)
			}
			if got := slices.Contains(a.scrub, fixtureSecret); got != tt.secret {
				t.Errorf("scrub registers the literal = %v, want %v", got, tt.secret)
			}
		})
	}
}

// A malformed handoff is fatal: running a secret finding without its
// scrub list is the one unsafe outcome.
func TestMalformedSecretHandoffIsFatal(t *testing.T) {
	ws := newWorkspace(t)
	if err := os.WriteFile(filepath.Join(ws, "input", "secrets.json"), []byte("{nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := New(newConfig(t, ws, &out), &fakeExec{}).Run(context.Background()); err == nil {
		t.Fatal("Run() error = nil, want a fatal handoff error")
	}
	if evs := events(t, out.String()); len(evs) != 1 || evs[0].Type != envelope.TypeFatal {
		t.Errorf("events = %+v, want one fatal", evs)
	}
}

func TestSliceSecret(t *testing.T) {
	dir := t.TempDir()
	content := "a: \"abcdefghij0123\"\n" +
		"-----BEGIN KEY-----\nMIIBOgIBAAJBAKj34GkxFhD9\nZZZZ-----END KEY-----\n"
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		loc  source.Location
		want []string
	}{
		{
			// Columns 5..19 cover the value; whether 19 is inclusive (the
			// closing quote) or exclusive, the bare value is registered.
			name: "single line, both end readings",
			loc:  source.Location{Path: "f.txt", StartLine: 1, EndLine: 1, StartColumn: 5, EndColumn: 19},
			want: []string{`abcdefghij0123"`, "abcdefghij0123"},
		},
		{
			name: "leading slash is repository-relative",
			loc:  source.Location{Path: "/f.txt", StartLine: 1, EndLine: 1, StartColumn: 5, EndColumn: 18},
			want: []string{"abcdefghij0123", "abcdefghij012"},
		},
		{
			name: "no columns uses the whole line",
			loc:  source.Location{Path: "f.txt", StartLine: 1},
			want: []string{`a: "abcdefghij0123"`},
		},
		{
			name: "multi-line registers the span and each line",
			loc:  source.Location{Path: "f.txt", StartLine: 2, EndLine: 4, StartColumn: 1, EndColumn: 4},
			want: []string{
				"-----BEGIN KEY-----\nMIIBOgIBAAJBAKj34GkxFhD9\nZZZZ",
				"-----BEGIN KEY-----\nMIIBOgIBAAJBAKj34GkxFhD9\nZZZ",
				"MIIBOgIBAAJBAKj34GkxFhD9",
				"-----BEGIN KEY-----",
				"ZZZZ",
			},
		},
		{name: "escaping path", loc: source.Location{Path: "../f.txt", StartLine: 1}},
		{name: "missing file", loc: source.Location{Path: "gone.txt", StartLine: 1}},
		{name: "line past the end", loc: source.Location{Path: "f.txt", StartLine: 40}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sliceSecret(dir, tt.loc)
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("sliceSecret() = %q, want %q", got, tt.want)
			}
		})
	}
}
