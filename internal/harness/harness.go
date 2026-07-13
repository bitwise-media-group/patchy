// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package harness

import (
	"os/exec"

	"github.com/bitwise-media-group/patchy/internal/runner"
)

// Usage is the harness-reported consumption of one agent session. Fields are
// nil where the CLI does not report them. InputTokens is the fresh (uncached)
// input only; cache reads and writes are reported separately so a multi-turn
// session's cheap cache traffic does not inflate the headline input figure.
type Usage struct {
	InputTokens         *int // fresh (uncached) input only
	CacheReadTokens     *int
	CacheCreationTokens *int
	OutputTokens        *int
	CostUSD             *float64
}

// PromptRequest describes one prompted agent run, harness-agnostically; the
// harness maps it onto its CLI's flags.
type PromptRequest struct {
	Prompt             string
	Model              string   // harness-specific model id
	SystemPromptAppend string   // appended to the CLI's system prompt when set
	SessionID          string   // pre-assigned UUID → claude --session-id
	MaxTurns           int      // agent-turn ceiling; 0 leaves the CLI default
	AllowedTools       []string // tool grammar entries, space-joined for claude
	DisallowedTools    []string
	AddDirs            []string // extra directories the agent may access
	Env                []string // extras appended to os.Environ() by the runner
}

// AgentResult is the parsed terminal result of one agent run.
type AgentResult struct {
	FinalText string
	SessionID string // the session id the CLI reports on its result event
	NumTurns  int
	Usage     *Usage // includes CostUSD from total_cost_usd; nil when unreported
	IsError   bool
	Subtype   string
	Errors    []string
}

// Harness is the required surface every agent CLI implements. A harness
// drives a model — it does not own one; the model a run targets is supplied
// as a harness-specific CLI id on the PromptRequest.
type Harness interface {
	ID() string        // registry key, e.g. "claude"
	Name() string      // human name, e.g. "Claude Code"
	CLI() []string     // runner binary candidates, in preference order
	EnvKeys() []string // credential env vars, in preference order
	// PromptSpec builds the headless command for one prompted run in workspace ws.
	PromptSpec(ws string, req PromptRequest) runner.CommandSpec
	// ParseResult extracts the terminal result from the CLI's full stdout.
	// ok is false when the output carried no terminal result event (plain
	// text, crash mid-stream); the AgentResult then carries the raw stdout as
	// FinalText and nothing else.
	ParseResult(stdout []byte) (res AgentResult, ok bool)
	// RuntimeError returns a short reason when the agent run produced no
	// usable output (auth blocked, crash, empty/error envelope), or "" when
	// the output is usable. A benign non-zero exit (e.g. max-turns) that
	// still produced a result returns "" — it is a partial answer, not an
	// error.
	RuntimeError(stdout []byte, exitCode int, timedOut bool) string
}

// UsageScanner is the optional capability of reading token usage off the live
// output stream; it powers the caller's output-token budget kill switch.
type UsageScanner interface {
	// ScanUsage returns the output-token count reported by one stream line
	// (claude: assistant events' message.usage.output_tokens), ok=false
	// otherwise.
	ScanUsage(line []byte) (outputTokens int, ok bool)
}

// All returns the builtin harness set.
func All() []Harness {
	return []Harness{NewClaude(), NewFake()}
}

// ByID returns the builtin harness with the given id, if any.
func ByID(id string) (Harness, bool) {
	for _, h := range All() {
		if h.ID() == id {
			return h, true
		}
	}
	return nil, false
}

// Available finds the first of the harness's runner binaries on PATH.
func Available(h Harness) (path string, ok bool) {
	for _, name := range h.CLI() {
		if p, err := exec.LookPath(name); err == nil {
			return p, true
		}
	}
	return "", false
}

// base carries the descriptive fields shared by all harnesses.
type base struct {
	id      string
	name    string
	clis    []string
	envKeys []string
}

func (b base) ID() string        { return b.id }
func (b base) Name() string      { return b.name }
func (b base) CLI() []string     { return b.clis }
func (b base) EnvKeys() []string { return b.envKeys }
