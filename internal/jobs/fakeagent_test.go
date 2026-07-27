// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package jobs

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bitwise-media-group/patchy/internal/envelope"
	"github.com/bitwise-media-group/patchy/internal/transcript"
)

// hack/fake-agent is the credential-less agent stand-in the dev-fake overlay
// runs (see its header). It hand-writes the two stdout schemas this package
// decodes, so it can only be kept honest by decoding its real output: nothing
// else in the build reaches a shell script. Both failure modes are silent at
// the source — a stale envelope version fails every dev run with "agent job
// produced no <stage> event", a stale turn version drops the conversation
// while the run stays green — which is exactly why this is a test and not a
// comment asking the next person to remember.
const fakeAgentScript = "../../hack/fake-agent/agent-runner"

// runFakeAgent executes the script for one phase and returns what the
// controller's log scan makes of its output.
func runFakeAgent(t *testing.T, phase string) RunOutput {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake agent is a POSIX shell script")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on PATH")
	}
	script, err := filepath.Abs(fakeAgentScript)
	if err != nil {
		t.Fatalf("resolve script: %v", err)
	}
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("stat script: %v", err)
	}

	cmd := exec.Command(sh, script)
	cmd.Env = append(os.Environ(),
		"PATCHY_PHASE="+phase,
		"PATCHY_REPO=acme/shop",
		"PATCHY_FINDING=finding-1",
		"PATCHY_BASE_SHA=00000000000000000000000000000000000000ba",
		"PATCHY_INVESTIGATE_MODEL=anthropic/claude-sonnet-5",
		"PATCHY_REMEDIATE_MODEL=anthropic/claude-sonnet-5",
		// The script paces its turns to make the live stream watchable; a
		// test has nothing to watch.
		"PATCHY_FAKE_TURN_DELAY=0",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("run fake agent (%s): %v: %s", phase, err, stderr.String())
	}

	var out RunOutput
	err = scanLog(&stdout, func(e envelope.Event) error {
		out.Events = append(out.Events, e)
		return nil
	}, func(tn transcript.Turn) error {
		out.Turns = append(out.Turns, tn)
		return nil
	})
	if err != nil {
		t.Fatalf("scan fake agent output (%s): %v", phase, err)
	}
	return out
}

// checkInvestigation asserts the analysis-stage payload and returns the
// fields every stage shares.
func checkInvestigation(t *testing.T, ev envelope.Event) (envelope.Stage, string) {
	t.Helper()
	inv := ev.Investigation
	if inv == nil {
		t.Fatal("investigation payload is nil")
	}
	// The demo route: a confident remediate verdict, so a finding walks all
	// the way to a pull request without a human.
	if inv.Recommendation != "remediate" {
		t.Errorf("recommendation = %q, want remediate", inv.Recommendation)
	}
	if inv.Confidence <= 0 || inv.Confidence > 1 {
		t.Errorf("confidence = %v, want (0,1]", inv.Confidence)
	}
	// v4 renamed these; under the v3 names they decode as zero and the
	// estimate the approval gate reads silently disappears.
	if inv.EstimatedMaxTurns <= 0 || inv.EstimatedTokenBudget <= 0 {
		t.Errorf("estimate = %d turns / %d tokens, want both > 0",
			inv.EstimatedMaxTurns, inv.EstimatedTokenBudget)
	}
	if inv.Exploitability.Rating == "" || inv.Likelihood.Rating == "" || inv.Impact.Rating == "" {
		t.Error("an analysis dimension is unrated; the priority score reads all three")
	}
	return inv.Stage, inv.ReportMarkdown
}

// checkRemediation asserts the fix-stage payload and returns the fields every
// stage shares.
func checkRemediation(t *testing.T, ev envelope.Event) (envelope.Stage, string) {
	t.Helper()
	rem := ev.Remediation
	if rem == nil {
		t.Fatal("remediation payload is nil")
	}
	if !rem.Success {
		t.Error("success = false, want true")
	}
	if rem.Changeset == nil || len(rem.Changeset.Upserts) == 0 {
		t.Fatal("changeset carries no upserts; there would be nothing to push")
	}
	if rem.Changeset.BaseSHA == "" {
		t.Error("changeset base SHA is empty")
	}
	return rem.Stage, rem.ReportMarkdown
}

func TestFakeAgentStageResults(t *testing.T) {
	tests := []struct {
		name  string
		phase string
		want  envelope.Type
		check func(*testing.T, envelope.Event) (envelope.Stage, string)
	}{
		{"investigate", "investigate", envelope.TypeInvestigation, checkInvestigation},
		{"remediate", "remediate", envelope.TypeRemediation, checkRemediation},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := runFakeAgent(t, tc.phase)

			// The whole point: Decode drops any line whose version is not the
			// current one, so a stale script yields zero events here.
			if len(out.Events) != 1 {
				t.Fatalf("events = %d, want exactly 1 (stale envelope version in %s?)",
					len(out.Events), fakeAgentScript)
			}
			ev := out.Events[0]
			if ev.Type != tc.want {
				t.Fatalf("event type = %q, want %q", ev.Type, tc.want)
			}
			if ev.Repo != "acme/shop" || ev.Finding != "finding-1" {
				t.Errorf("event context = %q/%q, want acme/shop/finding-1", ev.Repo, ev.Finding)
			}

			stage, report := tc.check(t, ev)
			if stage.Outcome != envelope.OutcomeOK {
				t.Errorf("outcome = %q, want ok", stage.Outcome)
			}
			if stage.Harness != "fake" {
				t.Errorf("harness = %q, want fake", stage.Harness)
			}
			// An empty model leaves the per-model rollup without a scope key.
			if stage.Model == "" {
				t.Error("model is empty")
			}
			if !strings.Contains(report, "##") {
				t.Errorf("report markdown does not look like a report: %q", report)
			}
		})
	}
}

func TestFakeAgentTranscript(t *testing.T) {
	for _, phase := range []string{"investigate", "remediate"} {
		t.Run(phase, func(t *testing.T) {
			out := runFakeAgent(t, phase)

			// A stale turn schema decodes as nothing at all, and the run still
			// succeeds — the conversation just never reaches the status page.
			if len(out.Turns) == 0 {
				t.Fatalf("no turns decoded (stale turn version in %s?)", fakeAgentScript)
			}

			kinds := map[transcript.Kind]bool{}
			for i, tn := range out.Turns {
				if tn.Seq != i+1 {
					t.Errorf("turn %d has seq %d; the status page orders on it", i, tn.Seq)
				}
				if tn.At == "" {
					t.Errorf("turn %d has no timestamp", tn.Seq)
				}
				switch tn.Role {
				case transcript.RoleAssistant, transcript.RoleUser, transcript.RoleSystem:
				default:
					t.Errorf("turn %d has role %q, outside the vocabulary", tn.Seq, tn.Role)
				}
				if tn.Kind == transcript.KindToolUse || tn.Kind == transcript.KindToolResult {
					if tn.Tool == "" {
						t.Errorf("turn %d is a tool turn with no tool name", tn.Seq)
					}
				}
				kinds[tn.Kind] = true
			}

			// The renderer branches per kind, so a demo that exercises only
			// one of them is not much of a demo.
			for _, want := range []transcript.Kind{
				transcript.KindNotice, transcript.KindThinking,
				transcript.KindText, transcript.KindToolUse, transcript.KindToolResult,
			} {
				if !kinds[want] {
					t.Errorf("no %q turn in the %s conversation", want, phase)
				}
			}

			// The result's turn count is what the UI shows beside the
			// conversation before opening it.
			var reported int
			switch {
			case len(out.Events) != 1:
				t.Fatalf("events = %d, want exactly 1", len(out.Events))
			case out.Events[0].Investigation != nil:
				reported = out.Events[0].Investigation.NumTurns
			case out.Events[0].Remediation != nil:
				reported = out.Events[0].Remediation.NumTurns
			}
			if reported != len(out.Turns) {
				t.Errorf("event reports %d turns, transcript carries %d", reported, len(out.Turns))
			}
		})
	}
}

func TestFakeAgentUnknownPhase(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake agent is a POSIX shell script")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on PATH")
	}
	script, err := filepath.Abs(fakeAgentScript)
	if err != nil {
		t.Fatalf("resolve script: %v", err)
	}

	cmd := exec.Command(sh, script)
	cmd.Env = append(os.Environ(), "PATCHY_PHASE=bogus", "PATCHY_FAKE_TURN_DELAY=0")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err == nil {
		t.Error("exit code = 0 for an unknown phase, want non-zero")
	}

	// A fatal event is how the controller learns the stage died; an unknown
	// phase that only exited non-zero would abort with no explanation.
	ev, ok := envelope.Decode(bytes.TrimSpace(stdout.Bytes()))
	if !ok {
		t.Fatalf("no decodable event for an unknown phase: %q", stdout.String())
	}
	if ev.Type != envelope.TypeFatal {
		t.Errorf("event type = %q, want fatal", ev.Type)
	}
	if ev.Error == "" {
		t.Error("fatal event carries no error text")
	}
}
