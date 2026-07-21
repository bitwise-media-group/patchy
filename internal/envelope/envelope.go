// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package envelope

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Prefix marks an event line on the agent-runner's stdout.
const Prefix = "PATCHY-EVENT: "

// Version is the current envelope schema version. Version 2 replaced the
// git-bundle payload with the structured Changeset; version 3 added the
// investigation event (exploitability/likelihood/impact analyses) and keys
// events by finding name.
const Version = 3

// Type discriminates events.
type Type string

// The event types: one per completed stage, plus fatal for a runner that
// could not produce a stage result at all.
const (
	TypeInvestigation Type = "investigation"
	TypeRemediation   Type = "remediation"
	TypeFatal         Type = "fatal"
)

// Outcome describes how a stage ended.
type Outcome string

// Stage outcomes. Only OutcomeOK carries a trusted report; every other
// outcome routes the issue to humans.
const (
	OutcomeOK                Outcome = "ok"
	OutcomeRuntimeError      Outcome = "runtime_error"
	OutcomeTimeout           Outcome = "timeout"
	OutcomeBudgetExceeded    Outcome = "budget_exceeded"
	OutcomeReportMissing     Outcome = "report_missing"
	OutcomeReportInvalid     Outcome = "report_invalid"
	OutcomeCommitFailed      Outcome = "commit_failed"
	OutcomeChangesetTooLarge Outcome = "changeset_too_large"
)

// Usage is the stage's agent accounting (all fields concrete: the envelope
// reports what was measured, zeros where the harness didn't say).
type Usage struct {
	InputTokens         int     `json:"input_tokens"`
	OutputTokens        int     `json:"output_tokens"`
	CacheReadTokens     int     `json:"cache_read_tokens"`
	CacheCreationTokens int     `json:"cache_creation_tokens"`
	CostUSD             float64 `json:"cost_usd"`
}

// Stage carries what every stage reports regardless of kind.
type Stage struct {
	Outcome        Outcome `json:"outcome"`
	Harness        string  `json:"harness"`
	Model          string  `json:"model"`
	SessionID      string  `json:"session_id,omitempty"`
	NumTurns       int     `json:"num_turns,omitempty"`
	Usage          Usage   `json:"usage"`
	ElapsedSeconds float64 `json:"elapsed_seconds"`
	// Detail explains a non-ok outcome for humans.
	Detail string `json:"detail,omitempty"`
}

// AnalysisResult is one investigation dimension: a rating plus a short
// justification (the full reasoning lives in the report markdown).
type AnalysisResult struct {
	// Rating is none|low|medium|high|critical.
	Rating string `json:"rating"`
	// Summary is the agent's short justification.
	Summary string `json:"summary,omitempty"`
}

// Investigation is the analysis-stage event payload (version 3): the agent's
// exploitability, likelihood, and impact assessments plus its verdict. The
// runner never decides continuation — the controller routes on the verdict.
type Investigation struct {
	Stage
	ReportMarkdown string         `json:"report_markdown,omitempty"`
	Exploitability AnalysisResult `json:"exploitability"`
	Likelihood     AnalysisResult `json:"likelihood"`
	Impact         AnalysisResult `json:"impact"`
	Recommendation string         `json:"recommendation,omitempty"`
	Priority       string         `json:"priority,omitempty"`
	Severity       string         `json:"severity,omitempty"`
	Confidence     float64        `json:"confidence,omitempty"`
	// RemediationModel/MaxTurns/TokenBudget are the clamped stage-2
	// parameters the analysis suggested (the controller re-clamps before
	// launching the remediation job).
	RemediationModel string `json:"remediation_model,omitempty"`
	MaxTurns         int    `json:"max_turns,omitempty"`
	TokenBudget      int    `json:"token_budget,omitempty"`
	// AwaitApproval marks the breaking-change hold (a better-but-breaking
	// fix exists): remediation waits for a human approval.
	AwaitApproval bool `json:"await_approval"`
}

// FileChange is one file created or modified on the remediation branch.
type FileChange struct {
	Path string `json:"path"`
	// Mode is the git file mode: "100644", "100755", or "120000".
	Mode string `json:"mode"`
	// ContentB64 is the base64-encoded blob; for a symlink ("120000") it is
	// the encoded link target.
	ContentB64 string `json:"content_b64"`
}

// Changeset expresses the agent's committed change as file contents so the
// controller can push it through the GitHub API without git.
type Changeset struct {
	// BaseSHA is the commit the clone was pinned to; the pushed commit's
	// parent.
	BaseSHA string `json:"base_sha"`
	// CommitMessage carries the agent's commit message(s); multiple local
	// commits squash into one API commit.
	CommitMessage string       `json:"commit_message"`
	Upserts       []FileChange `json:"upserts,omitempty"`
	Deletes       []string     `json:"deletes,omitempty"`
}

// Remediation is the stage-2 event payload.
type Remediation struct {
	Stage
	ReportMarkdown string  `json:"report_markdown,omitempty"`
	Success        bool    `json:"success"`
	Confidence     float64 `json:"confidence,omitempty"`
	// Branch is the local branch carrying the fix; Changeset is its content
	// diffed against Changeset.BaseSHA.
	Branch    string     `json:"branch,omitempty"`
	Changeset *Changeset `json:"changeset,omitempty"`
}

// Event is one envelope line.
type Event struct {
	V    int  `json:"v"`
	Type Type `json:"type"`
	// Finding context so events are self-contained.
	Repo string `json:"repo"`
	// Finding names the owning Finding resource.
	Finding string `json:"finding,omitempty"`

	Investigation *Investigation `json:"investigation,omitempty"`
	Remediation   *Remediation   `json:"remediation,omitempty"`
	// Error is set on fatal events.
	Error string `json:"error,omitempty"`
}

// Encode renders the event as one stdout line (prefix included, newline
// excluded).
func (e Event) Encode() (string, error) {
	e.V = Version
	raw, err := json.Marshal(e)
	if err != nil {
		return "", fmt.Errorf("envelope: encode: %w", err)
	}
	return Prefix + string(raw), nil
}

// Decode recovers an event from one log line; ok is false for any line that
// is not an envelope event.
func Decode(line []byte) (Event, bool) {
	rest, found := bytes.CutPrefix(bytes.TrimSpace(line), []byte(Prefix))
	if !found {
		// Kubernetes log lines may carry timestamps or the runtime may
		// have wrapped the line; find the prefix anywhere.
		if i := strings.Index(string(line), Prefix); i >= 0 {
			rest = line[i+len(Prefix):]
		} else {
			return Event{}, false
		}
	}
	var e Event
	if err := json.Unmarshal(rest, &e); err != nil {
		return Event{}, false
	}
	if e.V != Version || e.Type == "" {
		return Event{}, false
	}
	return e, true
}
