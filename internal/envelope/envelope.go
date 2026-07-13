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

// Version is the current envelope schema version.
const Version = 1

// Type discriminates events.
type Type string

// The event types: one per completed stage, plus fatal for a runner that
// could not produce a stage result at all.
const (
	TypeClassification Type = "classification"
	TypeRemediation    Type = "remediation"
	TypeFatal          Type = "fatal"
)

// Outcome describes how a stage ended.
type Outcome string

// Stage outcomes. Only OutcomeOK carries a trusted report; every other
// outcome routes the issue to humans.
const (
	OutcomeOK             Outcome = "ok"
	OutcomeRuntimeError   Outcome = "runtime_error"
	OutcomeTimeout        Outcome = "timeout"
	OutcomeBudgetExceeded Outcome = "budget_exceeded"
	OutcomeReportMissing  Outcome = "report_missing"
	OutcomeReportInvalid  Outcome = "report_invalid"
	OutcomeCommitFailed   Outcome = "commit_failed"
	OutcomeBundleTooLarge Outcome = "bundle_too_large"
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

// Classification is the stage-1 event payload.
type Classification struct {
	Stage
	ReportMarkdown string  `json:"report_markdown,omitempty"`
	Recommendation string  `json:"recommendation,omitempty"`
	Priority       string  `json:"priority,omitempty"`
	Severity       string  `json:"severity,omitempty"`
	Confidence     float64 `json:"confidence,omitempty"`
	// RemediationModel/MaxTurns/TokenBudget are the CLAMPED stage-2
	// parameters (allowlist and ceilings applied), not the raw suggestion.
	RemediationModel string `json:"remediation_model,omitempty"`
	MaxTurns         int    `json:"max_turns,omitempty"`
	TokenBudget      int    `json:"token_budget,omitempty"`
	// WillRemediate is the runner's local decision to continue to stage 2.
	WillRemediate bool `json:"will_remediate"`
	// AwaitApproval marks the breaking-change hold (a better-but-breaking
	// fix exists): remediation waits for a human /approve.
	AwaitApproval bool `json:"await_approval"`
}

// Remediation is the stage-2 event payload.
type Remediation struct {
	Stage
	ReportMarkdown string  `json:"report_markdown,omitempty"`
	Success        bool    `json:"success"`
	Confidence     float64 `json:"confidence,omitempty"`
	// Branch is the local branch carrying the fix; BundleB64 is the
	// base64-encoded git bundle of default..branch.
	Branch    string `json:"branch,omitempty"`
	BundleB64 string `json:"bundle_b64,omitempty"`
}

// Event is one envelope line.
type Event struct {
	V    int  `json:"v"`
	Type Type `json:"type"`
	// Issue context so events are self-contained.
	Repo  string `json:"repo"`
	Issue int    `json:"issue"`

	Classification *Classification `json:"classification,omitempty"`
	Remediation    *Remediation    `json:"remediation,omitempty"`
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
