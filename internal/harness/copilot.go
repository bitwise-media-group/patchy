// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package harness

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/bitwise-media-group/patchy/internal/runner"
	"github.com/bitwise-media-group/patchy/internal/transcript"
)

// Copilot drives GitHub's agentic Copilot CLI (`copilot -p`, the
// non-interactive runner — not the older `gh copilot` suggest/explain
// extension). It is the one harness that is not vendor-native: Copilot brokers
// both vendors' models, so it can run any model in the registry, and it is
// every model's fallback rather than any model's preferred harness.
//
// Its credential is the reason for that ordering. claude and codex take a model
// API key scoped to inference; copilot authenticates with a GitHub token
// (COPILOT_GITHUB_TOKEN/GH_TOKEN/GITHUB_TOKEN), which is a forge credential in
// a pod whose entire isolation model rests on never holding one. PromptSpec
// therefore disables the built-in GitHub MCP server on every run so nothing in
// the agent can spend that token against the GitHub API, and the egress policy
// for a copilot pod admits only the Copilot endpoints. The token is still the
// broadest credential any agent pod carries, so the runner ships disabled and
// an operator opts into it deliberately.
type Copilot struct {
	base
}

// NewCopilot returns the builtin Copilot harness.
func NewCopilot() *Copilot {
	return &Copilot{base: base{
		id:   "copilot",
		name: "GitHub Copilot",
		clis: []string{"copilot"},
		// Non-interactive auth precedence, highest first, as the CLI itself
		// resolves it (`copilot help environment`). Each is a GitHub token, not
		// a model API key — see the type comment.
		envKeys: []string{"COPILOT_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"},
	}}
}

// copilotDeny renders each sandbox posture into Copilot's permission grammar.
// Only URL access is denied, and in both postures: the pod has no egress beyond
// the model API and the stages never fetch, so this closes the one hole
// --allow-all-tools would otherwise open (the web-fetch tool and shell URL
// access).
//
// Write denial is deliberately absent. Copilot's grammar resolves deny over
// allow unconditionally — "denial rules always take precedence over allow
// rules, even --allow-all-tools" — so there is no way to express the
// investigation posture's actual requirement, "no source edits but do write the
// report": a write(...) deny wide enough to stop edits also stops the report
// the stage exists to produce, and a narrower deny would leave the edits it was
// meant to prevent. The read-only posture is therefore enforced where it
// already is for codex — the pod (no egress, no forge credential, workspace
// discarded after the run) and the fact that only the remediation stage's
// changeset is ever pushed. SandboxDefault renders nothing, matching the other
// harnesses.
var copilotDeny = map[Sandbox][]string{
	SandboxReadOnly:       {"url()"},
	SandboxWorkspaceWrite: {"url()"},
}

// PromptSpec builds the headless copilot invocation for one prompted run.
// --output-format json emits the session-event stream as JSONL, one object per
// line, which is what ParseResult and ScanUsage read; --allow-all-tools is what
// the CLI requires for a non-interactive run and --no-ask-user keeps the agent
// from stalling on a question nobody will answer.
//
// Three flags exist for the pod rather than the prompt. --disable-builtin-mcps
// stops the bundled github-mcp-server from spending the GitHub token the CLI
// authenticated with (see the type comment). --no-remote and --no-remote-export
// keep the session off GitHub's web and mobile surfaces: patchy's sessions carry
// unremediated vulnerability detail, which must not leave the pod through a
// side channel the operator never opted into. --no-auto-update is belt and
// braces over the image's COPILOT_AUTO_UPDATE=false — the rootfs is read-only
// and the version is pinned.
//
// MaxTurns does not map: copilot bounds work by AI credits and autopilot
// continues, neither of which is a turn ceiling, so the budget and timeout stay
// with the runner exactly as they do for codex. SystemPromptAppend has no
// system-prompt channel either and is folded into the prompt so its
// instructions still reach the agent.
func (c *Copilot) PromptSpec(ws string, req PromptRequest) runner.CommandSpec {
	prompt := req.Prompt
	if req.SystemPromptAppend != "" {
		prompt = req.SystemPromptAppend + "\n\n" + prompt
	}
	argv := []string{
		"copilot", "-p", prompt,
		"--model", req.Model,
		"--output-format", "json",
		"--allow-all-tools",
		"--no-ask-user",
		"--disable-builtin-mcps",
		"--no-remote",
		"--no-remote-export",
		"--no-auto-update",
	}
	for _, pattern := range copilotDeny[req.Sandbox] {
		argv = append(argv, "--deny-tool", pattern)
	}
	for _, dir := range req.AddDirs {
		argv = append(argv, "--add-dir", dir)
	}
	if req.SessionID != "" {
		argv = append(argv, "--session-id", req.SessionID)
	}
	return runner.CommandSpec{Argv: argv, Dir: ws, Env: req.Env}
}

// copilotEvent is one line of `copilot --output-format json` output: a session
// event ({type, timestamp, data}) or the terminal result the CLI appends when
// the prompt-mode run finishes. Each type populates only its own fields.
//
// The terminal event is type:"result", carrying the session id and the process
// exit code it is about to exit with; its usage block reports premium requests
// and durations, not tokens, so token accounting comes from the assistant.usage
// events instead.
type copilotEvent struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionId"`
	ExitCode  *int   `json:"exitCode"`
	Data      struct {
		// assistant.message
		Content string `json:"content"`
		// assistant.usage
		InputTokens      *int `json:"inputTokens"`
		OutputTokens     *int `json:"outputTokens"`
		CacheReadTokens  *int `json:"cacheReadTokens"`
		CacheWriteTokens *int `json:"cacheWriteTokens"`
		// session.error / session.warning
		ErrorType string `json:"errorType"`
		Message   string `json:"message"`
	} `json:"data"`
}

// ScanTurns projects one copilot event line onto the transcript vocabulary.
//
// Messages only, deliberately: copilot ships disabled (see the Copilot type
// comment), and its tool-invocation event names are not pinned anywhere this
// repo can verify. Guessing at them would produce a transcript that silently
// omits or mislabels what the agent ran, which is worse than one that plainly
// carries only the prose. Widen this — with a captured stream to check against
// — when the copilot runner is enabled.
func (c *Copilot) ScanTurns(line []byte) []transcript.Turn {
	var ev copilotEvent
	if json.Unmarshal(line, &ev) != nil {
		return nil
	}
	switch ev.Type {
	case "session.start":
		if ev.SessionID == "" {
			return nil
		}
		return []transcript.Turn{{
			Role: transcript.RoleSystem, Kind: transcript.KindNotice,
			Text: "session " + ev.SessionID + " started",
		}}
	case "assistant.message":
		if strings.TrimSpace(ev.Data.Content) == "" {
			return nil
		}
		return []transcript.Turn{{
			Role: transcript.RoleAssistant, Kind: transcript.KindText, Text: ev.Data.Content,
		}}
	case "session.error":
		if strings.TrimSpace(ev.Data.Message) == "" {
			return nil
		}
		return []transcript.Turn{{
			Role: transcript.RoleSystem, Kind: transcript.KindNotice,
			Text: "error: " + strings.TrimSpace(ev.Data.Message),
		}}
	}
	return nil
}

// copilotScan is the digest of one event stream; ParseResult and RuntimeError
// each project from it.
type copilotScan struct {
	sessionID string
	texts     []string
	turns     int // assistant.turn_end events
	errors    []string
	terminal  bool // saw the type:"result" event
	exitCode  int
	usage     *Usage
}

// scanCopilotEvents walks the event stream once.
//
// Usage sums the per-call assistant.usage events. Copilot reports inputTokens
// as the whole prompt with cacheReadTokens and cacheWriteTokens as subsets of
// it — its own accounting derives the uncached figure as
// input-cacheRead-cacheWrite — while the Usage contract wants fresh (uncached)
// input on InputTokens and cache traffic reported separately. The cached
// portions are therefore split back out, the same correction the codex harness
// makes, so a long cached session does not report input many-fold over what was
// actually billed at the fresh rate.
//
// Copilot prices in premium requests rather than dollars, so CostUSD stays nil
// and the model registry's per-token rates price the run (agentresult.stageCost).
func scanCopilotEvents(stdout []byte) copilotScan {
	var s copilotScan
	var fresh, cacheRead, cacheCreation, output int
	var sawUsage bool
	for line := range bytes.SplitSeq(stdout, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var ev copilotEvent
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		switch ev.Type {
		case "session.start":
			if ev.SessionID != "" {
				s.sessionID = ev.SessionID
			}
		case "assistant.message":
			if txt := strings.TrimSpace(ev.Data.Content); txt != "" {
				s.texts = append(s.texts, txt)
			}
		case "assistant.turn_end":
			s.turns++
		case "assistant.usage":
			sawUsage = true
			read, write := intOrZero(ev.Data.CacheReadTokens), intOrZero(ev.Data.CacheWriteTokens)
			if in := intOrZero(ev.Data.InputTokens); in > 0 {
				fresh += max(0, in-read-write)
			}
			cacheRead += read
			cacheCreation += write
			output += intOrZero(ev.Data.OutputTokens)
		case "session.error":
			if msg := strings.TrimSpace(ev.Data.Message); msg != "" {
				s.errors = append(s.errors, msg)
			}
		case "result":
			s.terminal = true
			s.exitCode = intOrZero(ev.ExitCode)
			if ev.SessionID != "" {
				s.sessionID = ev.SessionID
			}
		}
	}
	if sawUsage {
		s.usage = &Usage{
			InputTokens:         &fresh,
			CacheReadTokens:     &cacheRead,
			CacheCreationTokens: &cacheCreation,
			OutputTokens:        &output,
		}
	}
	return s
}

// ParseResult digests the copilot event stream: the final answer is the
// concatenated assistant messages, the session id and exit code come off the
// terminal result event, and the turn count and usage accumulate over the
// stream. Output with no result event (an auth failure, which copilot reports
// as plain text before the stream ever starts, or a crash mid-stream) returns
// the raw stdout as FinalText with ok=false, matching the interface contract.
func (c *Copilot) ParseResult(stdout []byte) (AgentResult, bool) {
	s := scanCopilotEvents(stdout)
	if !s.terminal {
		return AgentResult{FinalText: string(stdout)}, false
	}
	return AgentResult{
		FinalText: strings.Join(s.texts, "\n"),
		SessionID: s.sessionID,
		NumTurns:  s.turns,
		Usage:     s.usage,
		IsError:   s.exitCode != 0 || len(s.errors) > 0,
		Errors:    s.errors,
	}, true
}

// RuntimeError detects a copilot run that produced no agent output (auth
// blocked, crash, failed session) so it is reported distinctly from a run whose
// report merely needs judging. A run that emitted any assistant message is
// usable regardless of exit code — a partial answer, not an error.
//
// The auth path is why the no-result case is an error rather than the
// "degenerate but usable" plain text the claude harness tolerates: copilot
// validates its token against the GitHub API before opening the stream and
// prints a plain-text failure, so stdout with no result event in it is a run
// that never started.
func (c *Copilot) RuntimeError(stdout []byte, exitCode int, timedOut bool) string {
	if len(bytes.TrimSpace(stdout)) == 0 {
		return "empty CLI output"
	}
	s := scanCopilotEvents(stdout)
	if len(s.texts) > 0 {
		return "" // produced agent output — usable
	}
	if len(s.errors) > 0 {
		return "copilot run error: " + strings.Join(s.errors, "; ")
	}
	switch {
	case timedOut:
		return "timed out with no agent output"
	case !s.terminal:
		return "copilot produced no result event: " + firstLine(stdout)
	case exitCode != 0 || s.exitCode != 0:
		return "copilot produced no agent output"
	}
	return ""
}

// ScanUsage reads the output-token count off one live stream line. Copilot
// reports usage per model call on assistant.usage, so the budget accumulator
// sees every call rather than only turn boundaries — a runaway single turn is
// killed mid-flight, which the codex stream cannot do.
func (c *Copilot) ScanUsage(line []byte) (int, bool) {
	var ev copilotEvent
	if json.Unmarshal(line, &ev) != nil {
		return 0, false
	}
	if ev.Type != "assistant.usage" || ev.Data.OutputTokens == nil {
		return 0, false
	}
	return *ev.Data.OutputTokens, true
}

// intOrZero reads an optional event counter, treating an absent field as zero:
// copilot omits a token field entirely when a call reports none.
func intOrZero(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// firstLine renders the head of unparseable output as one diagnostic line, so
// copilot's plain-text auth failures reach the operator instead of surfacing as
// a bare "no result event".
func firstLine(stdout []byte) string {
	for line := range bytes.SplitSeq(stdout, []byte{'\n'}) {
		if txt := strings.TrimSpace(string(line)); txt != "" {
			const maxLen = 200
			if len(txt) > maxLen {
				return txt[:maxLen] + "…"
			}
			return txt
		}
	}
	return "no output"
}
