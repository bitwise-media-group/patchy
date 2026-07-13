// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package harness

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/bitwise-media-group/patchy/internal/runner"
)

// Claude drives the `claude` CLI (Claude Code).
type Claude struct {
	base
}

// NewClaude returns the builtin Claude Code harness.
func NewClaude() *Claude {
	return &Claude{base: base{
		id:   "claude",
		name: "Claude Code",
		clis: []string{"claude"},
		// Credentials the claude CLI itself authenticates with. Both an
		// API-key and an OAuth-token form are accepted.
		envKeys: []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_AUTH_TOKEN"},
	}}
}

// PromptSpec builds the headless claude invocation for one prompted run.
// stream-json with --verbose emits one JSON event per line, which is what
// ParseResult and ScanUsage parse. Optional request fields append their flag
// only when set, in a stable order.
func (c *Claude) PromptSpec(ws string, req PromptRequest) runner.CommandSpec {
	argv := []string{
		"claude", "-p", req.Prompt,
		"--model", req.Model,
		"--output-format", "stream-json",
		"--verbose",
	}
	if req.MaxTurns > 0 {
		argv = append(argv, "--max-turns", strconv.Itoa(req.MaxTurns))
	}
	if len(req.AllowedTools) > 0 {
		argv = append(argv, "--allowedTools", strings.Join(req.AllowedTools, " "))
	}
	if len(req.DisallowedTools) > 0 {
		argv = append(argv, "--disallowedTools", strings.Join(req.DisallowedTools, " "))
	}
	for _, dir := range req.AddDirs {
		argv = append(argv, "--add-dir", dir)
	}
	if req.SessionID != "" {
		argv = append(argv, "--session-id", req.SessionID)
	}
	if req.SystemPromptAppend != "" {
		argv = append(argv, "--append-system-prompt", req.SystemPromptAppend)
	}
	return runner.CommandSpec{Argv: argv, Dir: ws, Env: req.Env}
}

// ParseResult reads the terminal result event from claude's stream-json
// output; see parseStreamResult.
func (c *Claude) ParseResult(stdout []byte) (AgentResult, bool) {
	return parseStreamResult(stdout)
}

// RuntimeError classifies claude's output; see streamRuntimeError.
func (c *Claude) RuntimeError(stdout []byte, exitCode int, timedOut bool) string {
	return streamRuntimeError(stdout, exitCode, timedOut)
}

// ScanUsage reads the output-token count off one live stream line; see
// scanStreamUsage.
func (c *Claude) ScanUsage(line []byte) (int, bool) {
	return scanStreamUsage(line)
}

// claudeUsage is the token accounting claude reports: on the terminal result
// event's top-level usage, and per assistant event under message.usage. Cache
// reads and writes are kept on their own fields; see parseStreamResult for
// why they are not folded into input.
type claudeUsage struct {
	InputTokens              int  `json:"input_tokens"`
	CacheCreationInputTokens int  `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int  `json:"cache_read_input_tokens"`
	OutputTokens             *int `json:"output_tokens"`
}

// claudeEvent is one line of Claude Code's stream-json (--verbose) output.
// Assistant events carry message.usage; the terminal type:"result" event
// carries the final answer, session id, turn count, usage, cost, and the
// error envelope (is_error/subtype/errors). Each event populates only its own
// fields, so the unused ones stay zero on the others.
type claudeEvent struct {
	Type    string `json:"type"`
	Message struct {
		Usage *claudeUsage `json:"usage"`
	} `json:"message"`
	Result       string       `json:"result"`
	SessionID    string       `json:"session_id"`
	NumTurns     int          `json:"num_turns"`
	IsError      bool         `json:"is_error"`
	Subtype      string       `json:"subtype"`
	Errors       []string     `json:"errors"`
	Usage        *claudeUsage `json:"usage"`
	TotalCostUSD *float64     `json:"total_cost_usd"`
}

// scanEvents walks stream-json output once and returns the terminal result
// event; found is false when the output carried none (plain text, or a crash
// mid-stream). parseStreamResult and streamRuntimeError each project from it.
func scanEvents(stdout []byte) (result claudeEvent, found bool) {
	for line := range bytes.SplitSeq(stdout, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var ev claudeEvent
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		if ev.Type == "result" {
			result, found = ev, true
		}
	}
	return result, found
}

// parseStreamResult reads the final answer, session id, turn count, and usage
// from the terminal result event of stream-json output. Cache writes and
// reads are reported on their own fields rather than folded into input: a
// multi-turn cached session re-reads the same base context every turn, so
// lumping cache reads into "input" inflates it many-fold over the (cheaply
// cached) reality. total_cost_usd still reflects everything the session
// consumed. Output with no result event (plain text, crash) returns the raw
// stdout as FinalText with ok=false.
func parseStreamResult(stdout []byte) (AgentResult, bool) {
	ev, found := scanEvents(stdout)
	if !found {
		return AgentResult{FinalText: string(stdout)}, false
	}
	res := AgentResult{
		FinalText: ev.Result,
		SessionID: ev.SessionID,
		NumTurns:  ev.NumTurns,
		IsError:   ev.IsError,
		Subtype:   ev.Subtype,
		Errors:    ev.Errors,
	}
	if ev.Usage != nil {
		in := ev.Usage.InputTokens
		cacheRead := ev.Usage.CacheReadInputTokens
		cacheCreation := ev.Usage.CacheCreationInputTokens
		res.Usage = &Usage{
			InputTokens:         &in,
			CacheReadTokens:     &cacheRead,
			CacheCreationTokens: &cacheCreation,
			OutputTokens:        ev.Usage.OutputTokens,
			CostUSD:             ev.TotalCostUSD,
		}
	}
	return res, true
}

// streamRuntimeError detects a run that produced no usable answer (auth
// blocked, init crash, error envelope without output) so it can be reported
// distinctly from a run that completed and merely needs its answer judged. A
// run with any non-empty result is usable — this deliberately includes
// max-turns/partial runs, which the CLI reports with is_error=true but a
// populated result.
func streamRuntimeError(stdout []byte, exitCode int, timedOut bool) string {
	if len(bytes.TrimSpace(stdout)) == 0 {
		return "empty CLI output"
	}
	result, found := scanEvents(stdout)
	if !found {
		switch {
		case timedOut:
			return "timed out with no result event"
		case exitCode != 0:
			return "unparseable CLI output"
		}
		return "" // a clean exit with plain-text output is degenerate but usable
	}
	if result.Result != "" {
		return "" // there is an answer to use (success, or a partial/max-turns run)
	}
	if result.IsError {
		return claudeErrorReason(result.Subtype, result.Errors)
	}
	return "" // empty-result success: usable (callers may inspect the workspace)
}

// scanStreamUsage reads the output-token count off one live stream line. Only
// assistant events carry message.usage; the result event's top-level usage
// (and every other event) reports ok=false so a budget accumulator never
// double-counts the terminal total.
func scanStreamUsage(line []byte) (int, bool) {
	var ev claudeEvent
	if json.Unmarshal(line, &ev) != nil {
		return 0, false
	}
	if ev.Type != "assistant" || ev.Message.Usage == nil || ev.Message.Usage.OutputTokens == nil {
		return 0, false
	}
	return *ev.Message.Usage.OutputTokens, true
}

// claudeErrorReason renders the claude error envelope into one diagnostic
// line. The claude CLI reports a failed run only on stdout: the subtype names
// the class (error_max_turns, error_during_execution) and the `errors` array
// carries the human-readable detail. Neither is ever written to stderr, so
// without lifting them here the run surfaces as a bare non-zero exit with no
// explanation.
func claudeErrorReason(subtype string, errs []string) string {
	reason := "claude run error"
	if subtype != "" {
		reason += " (" + subtype + ")"
	}
	cleaned := make([]string, 0, len(errs))
	for _, e := range errs {
		if e = strings.TrimSpace(e); e != "" {
			cleaned = append(cleaned, e)
		}
	}
	if len(cleaned) > 0 {
		reason += ": " + strings.Join(cleaned, "; ")
	}
	return reason
}
