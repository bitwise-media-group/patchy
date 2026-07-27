// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package agentrun

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/bitwise-media-group/patchy/internal/envelope"
	"github.com/bitwise-media-group/patchy/internal/harness"
	"github.com/bitwise-media-group/patchy/internal/transcript"
)

// turns decodes every transcript line from the runner's stdout.
func turns(t *testing.T, out string) []transcript.Turn {
	t.Helper()
	var got []transcript.Turn
	for _, line := range strings.Split(out, "\n") {
		if turn, ok := transcript.Decode([]byte(line)); ok {
			got = append(got, turn)
		}
	}
	return got
}

// conversation is a stream-json run that exercises every turn kind.
var conversation = []string{
	`{"type":"system","subtype":"init","session_id":"sess-1"}`,
	`{"type":"assistant","message":{"usage":{"output_tokens":5},"content":[` +
		`{"type":"text","text":"Reading the sink."}]}}`,
	`{"type":"assistant","message":{"usage":{"output_tokens":5},"content":[` +
		`{"type":"tool_use","name":"Read","input":{"file_path":"app.js"}}]}}`,
	`{"type":"user","message":{"content":[{"type":"tool_result","content":"vulnerable();"}]}}`,
}

func TestTranscriptTurnsReachStdout(t *testing.T) {
	ws := newWorkspace(t)
	var out bytes.Buffer
	fx := &fakeExec{steps: []step{
		{ws: ws, stdout: streamSuccess, budgetLines: conversation, writes: map[string]string{
			"reports/investigation.md": goodInvestigation,
		}},
	}}
	cfg := newConfig(t, ws, &out)
	if err := New(cfg, fx).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := turns(t, out.String())
	want := []struct {
		kind transcript.Kind
		text string
	}{
		{transcript.KindNotice, "session sess-1 started"},
		{transcript.KindText, "Reading the sink."},
		{transcript.KindToolUse, "app.js"},
		{transcript.KindToolResult, "vulnerable();"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d turns, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Kind != want[i].kind || got[i].Text != want[i].text {
			t.Errorf("turn %d = (%s, %q), want (%s, %q)",
				i, got[i].Kind, got[i].Text, want[i].kind, want[i].text)
		}
		if got[i].Seq != i+1 {
			t.Errorf("turn %d Seq = %d, want %d", i, got[i].Seq, i+1)
		}
	}
}

func TestTranscriptDoesNotDisturbEnvelope(t *testing.T) {
	// Turns and stage results share stdout under separate prefixes; neither
	// scanner may see the other's lines.
	ws := newWorkspace(t)
	var out bytes.Buffer
	fx := &fakeExec{steps: []step{
		{ws: ws, stdout: streamSuccess, budgetLines: conversation, writes: map[string]string{
			"reports/investigation.md": goodInvestigation,
		}},
	}}
	if err := New(newConfig(t, ws, &out), fx).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	evs := events(t, out.String())
	if len(evs) != 1 {
		t.Fatalf("got %d envelope events, want 1: %+v", len(evs), evs)
	}
	if evs[0].Type != envelope.TypeInvestigation {
		t.Errorf("event type = %s, want %s", evs[0].Type, envelope.TypeInvestigation)
	}
	if n := len(turns(t, out.String())); n != 4 {
		t.Errorf("got %d turns alongside the event, want 4", n)
	}
}

func TestTranscriptRecordsTheTurnThatTrippedTheBudget(t *testing.T) {
	// The transcript must explain why the run stopped, so the offending turn
	// is recorded before the budget check fires.
	ws := newWorkspace(t)
	var out bytes.Buffer
	fx := &fakeExec{steps: []step{{ws: ws, stdout: streamSuccess, budgetLines: []string{
		`{"type":"assistant","message":{"usage":{"output_tokens":10},"content":[` +
			`{"type":"text","text":"still going"}]}}`,
	}}}}
	cfg := newConfig(t, ws, &out)
	cfg.InvestigateTokenBudget = 5
	if err := New(cfg, fx).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := turns(t, out.String())
	if len(got) != 2 {
		t.Fatalf("got %d turns, want the offending turn plus the abort notice: %+v", len(got), got)
	}
	if got[0].Text != "still going" {
		t.Errorf("turn 0 = %q, want the turn that tripped the budget", got[0].Text)
	}
	if got[1].Kind != transcript.KindNotice || !strings.Contains(got[1].Text, "budget exceeded") {
		t.Errorf("turn 1 = %+v, want a budget notice", got[1])
	}

	evs := events(t, out.String())
	if len(evs) != 1 || evs[0].Investigation.Outcome != envelope.OutcomeBudgetExceeded {
		t.Errorf("outcome = %+v, want budget exceeded", evs)
	}
}

func TestTranscriptRedactsCredentials(t *testing.T) {
	const key = "sk-ant-not-a-real-key-value"
	t.Setenv("ANTHROPIC_API_KEY", key)

	ws := newWorkspace(t)
	var out bytes.Buffer
	fx := &fakeExec{steps: []step{
		{ws: ws, stdout: streamSuccess, budgetLines: []string{
			`{"type":"user","message":{"content":[{"type":"tool_result",` +
				`"content":"ANTHROPIC_API_KEY=` + key + `"}]}}`,
		}, writes: map[string]string{"reports/investigation.md": goodInvestigation}},
	}}
	if err := New(newConfig(t, ws, &out), fx).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if strings.Contains(out.String(), key) {
		t.Error("stdout carries the credential; the transcript must redact it")
	}
	got := turns(t, out.String())
	if len(got) != 1 || !strings.Contains(got[0].Text, transcript.Redacted) {
		t.Errorf("turns = %+v, want the value redacted", got)
	}
}

func TestObserveWithoutEitherCapabilityIsNil(t *testing.T) {
	// A harness with neither capability must leave the runner's fast path
	// untouched rather than paying for a no-op observer on every line.
	ws := newWorkspace(t)
	var out bytes.Buffer
	a := New(newConfig(t, ws, &out), &fakeExec{})
	onLine, rec := a.observe(bareHarness{}, 0)
	if onLine != nil || rec != nil {
		t.Errorf("observe(bare) = (%v, %v), want (nil, nil)", onLine != nil, rec != nil)
	}
}

// bareHarness implements Harness and neither optional capability.
type bareHarness struct{ harness.Harness }

func (bareHarness) ID() string        { return "bare" }
func (bareHarness) EnvKeys() []string { return nil }
