// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package harness

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/bitwise-media-group/patchy/internal/runner"
)

func TestFakePromptSpec(t *testing.T) {
	fixture := filepath.Join(t.TempDir(), "stream.jsonl")
	t.Setenv(FakeFixtureEnv, fixture)

	spec := NewFake().PromptSpec("/ws", PromptRequest{Prompt: "ignored", Env: []string{"A=b"}})
	if want := []string{"cat", fixture}; !slices.Equal(spec.Argv, want) {
		t.Errorf("Argv = %q, want %q", spec.Argv, want)
	}
	if spec.Dir != "/ws" || !slices.Equal(spec.Env, []string{"A=b"}) {
		t.Errorf("spec = %+v, want ws dir and request env preserved", spec)
	}
}

func TestFakeReplaysFixtureThroughRunner(t *testing.T) {
	// End to end: the fake spec runs through the real runner and its output
	// parses with the same stream-json helpers as claude's.
	fixture := filepath.Join(t.TempDir(), "stream.jsonl")
	if err := os.WriteFile(fixture, []byte(claudeStreamSuccess+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(FakeFixtureEnv, fixture)

	f := NewFake()
	spec := f.PromptSpec(t.TempDir(), PromptRequest{Prompt: "ignored"})
	out, err := (&runner.Exec{}).Run(context.Background(), spec, 5*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reason := f.RuntimeError(out.Stdout, out.ExitCode, out.TimedOut); reason != "" {
		t.Fatalf("RuntimeError = %q, want usable output", reason)
	}
	res, ok := f.ParseResult(out.Stdout)
	if !ok {
		t.Fatal("ParseResult ok = false, want the fixture's result event")
	}
	if res.FinalText != "Done." || res.SessionID != claudeSessionID || res.NumTurns != 7 {
		t.Errorf("res = %+v, want the fixture's terminal result", res)
	}
	if res.Usage == nil || derefInt(res.Usage.OutputTokens) != 30 {
		t.Errorf("Usage = %+v, want the fixture's usage", res.Usage)
	}
}
