// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package harness

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/bitwise-media-group/patchy/internal/runner"
)

// Codex drives the `codex` CLI (OpenAI Codex), the alternative harness for
// running the agent stages on OpenAI models.
type Codex struct {
	base
}

// NewCodex returns the builtin Codex harness.
func NewCodex() *Codex {
	return &Codex{base: base{
		id:   "codex",
		name: "OpenAI Codex",
		clis: []string{"codex"},
		// The credential the codex CLI authenticates with in headless mode.
		envKeys: []string{"OPENAI_API_KEY"},
	}}
}

// PromptSpec builds the headless codex invocation for one prompted run.
// `codex exec --json` emits one JSON event per line, which is what
// ParseResult and ScanUsage parse. Codex's own Landlock/Seatbelt sandbox is
// always disabled: patchy confines the agent at the pod layer (no network
// egress beyond the model API, no credentials, locked-down securityContext),
// where codex's kernel sandbox is redundant and unavailable anyway.
//
// codex exec has no equivalents for MaxTurns, AllowedTools/DisallowedTools,
// AddDirs, or a pre-assigned SessionID, so those request fields do not map;
// budget and timeout enforcement stay with the runner, and the session id is
// read back from the stream's thread.started event instead.
// SystemPromptAppend has no system-prompt channel either and is folded into
// the prompt so its instructions still reach the agent.
func (c *Codex) PromptSpec(ws string, req PromptRequest) runner.CommandSpec {
	prompt := req.Prompt
	if req.SystemPromptAppend != "" {
		prompt = req.SystemPromptAppend + "\n\n" + prompt
	}
	argv := []string{
		"codex", "exec", prompt,
		"--json",
		"--skip-git-repo-check",
		"--sandbox", "danger-full-access",
		"--model", req.Model,
	}
	return runner.CommandSpec{Argv: argv, Dir: ws, Env: req.Env}
}

// codexEvent is one line of `codex exec --json` output. Each event type
// populates only its own fields: thread.started carries the thread id,
// item.completed the agent messages, turn.completed the per-turn usage,
// turn.failed an error object, and type:"error" a bare message.
type codexEvent struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
	Item     struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"item"`
	Usage *struct {
		InputTokens       *int `json:"input_tokens"`
		CachedInputTokens *int `json:"cached_input_tokens"`
		OutputTokens      *int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
	Message string `json:"message"`
}

// codexScan is the digest of one event stream; ParseResult and RuntimeError
// each project from it.
type codexScan struct {
	threadID string
	texts    []string
	turns    int // completed turns
	errors   []string
	terminal bool // saw turn.completed or turn.failed
	usage    *Usage
}

// scanCodexEvents walks the event stream once. Usage is summed across
// turn.completed events; codex reports input_tokens as the whole prompt with
// cached_input_tokens a subset of it, and the Usage contract wants fresh
// (uncached) input on InputTokens with cache hits reported separately, so the
// cached portion is split off rather than letting re-read context inflate the
// headline input figure. Codex reports tokens but never cost, so CostUSD
// stays nil.
func scanCodexEvents(stdout []byte) codexScan {
	var s codexScan
	var fresh, cacheRead, output int
	for line := range bytes.SplitSeq(stdout, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var ev codexEvent
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		switch ev.Type {
		case "thread.started":
			s.threadID = ev.ThreadID
		case "item.completed":
			if ev.Item.Type == "agent_message" {
				s.texts = append(s.texts, ev.Item.Text)
			}
		case "turn.completed":
			s.turns++
			s.terminal = true
			if u := ev.Usage; u != nil {
				if u.InputTokens != nil {
					in := *u.InputTokens
					if u.CachedInputTokens != nil {
						read := min(*u.CachedInputTokens, in)
						in -= read
						cacheRead += read
					}
					fresh += in
				}
				if u.OutputTokens != nil {
					output += *u.OutputTokens
				}
				s.usage = &Usage{InputTokens: &fresh, CacheReadTokens: &cacheRead, OutputTokens: &output}
			}
		case "turn.failed":
			s.terminal = true
			if ev.Error != nil && strings.TrimSpace(ev.Error.Message) != "" {
				s.errors = append(s.errors, strings.TrimSpace(ev.Error.Message))
			}
		case "error":
			if strings.TrimSpace(ev.Message) != "" {
				s.errors = append(s.errors, strings.TrimSpace(ev.Message))
			}
		}
	}
	return s
}

// ParseResult digests the codex event stream: the final answer is the
// concatenated agent messages, the session id is the thread id, and the turn
// count and usage accumulate over completed turns. Output with no terminal
// turn event (plain text, crash mid-stream) returns the raw stdout as
// FinalText with ok=false, matching the interface contract.
func (c *Codex) ParseResult(stdout []byte) (AgentResult, bool) {
	s := scanCodexEvents(stdout)
	if !s.terminal {
		return AgentResult{FinalText: string(stdout)}, false
	}
	return AgentResult{
		FinalText: strings.Join(s.texts, "\n"),
		SessionID: s.threadID,
		NumTurns:  s.turns,
		Usage:     s.usage,
		IsError:   len(s.errors) > 0,
		Errors:    s.errors,
	}, true
}

// RuntimeError detects a codex run that produced no agent output (auth
// blocked, crash, failed turn) so it is reported distinctly from a run whose
// report merely needs judging. A run that emitted any agent message is
// usable regardless of exit code — a partial answer, not an error.
func (c *Codex) RuntimeError(stdout []byte, exitCode int, timedOut bool) string {
	if len(bytes.TrimSpace(stdout)) == 0 {
		return "empty CLI output"
	}
	s := scanCodexEvents(stdout)
	if len(s.texts) > 0 {
		return "" // produced agent output — usable
	}
	if len(s.errors) > 0 {
		return "codex run error: " + strings.Join(s.errors, "; ")
	}
	switch {
	case timedOut:
		return "timed out with no agent output"
	case exitCode != 0:
		return "codex produced no agent output"
	}
	return ""
}

// ScanUsage reads the output-token count off one live stream line. Codex
// reports usage only on turn.completed events, so the budget accumulator
// sums per-turn totals; within a turn the budget cannot fire early, but a
// multi-turn session over budget is still killed between turns.
func (c *Codex) ScanUsage(line []byte) (int, bool) {
	var ev codexEvent
	if json.Unmarshal(line, &ev) != nil {
		return 0, false
	}
	if ev.Type != "turn.completed" || ev.Usage == nil || ev.Usage.OutputTokens == nil {
		return 0, false
	}
	return *ev.Usage.OutputTokens, true
}
