// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package evaluation

// SSE event names on GET /api/v1/evaluations/{name}/events.
//
// The stream is content-bearing with an explicit end event:
//
//   - On connect the server replays one "unit" event per child, so a
//     reconnecting client rebuilds its view without Last-Event-ID; updates
//     re-emit "unit" and the client applies them idempotently by name.
//   - "event" events relay raw in-pod progress lines tagged with the unit
//     name. They are best-effort and degradable: a client must render
//     correctly from "unit" events alone.
//   - "end" carries the final snapshot; the server closes after sending it.
const (
	SSEEventUnit  = "unit"
	SSEEventEvent = "event"
	SSEEventEnd   = "end"
)

// UnitStatusWire is one child's state, sent as an SSE "unit" event and
// embedded in snapshots. Phase values are Pending|Running|Complete|Failed.
type UnitStatusWire struct {
	// Name of the EvaluationUnit; the client's idempotency key.
	Name string `json:"name"`
	// Index within the submission, 0-based.
	Index int    `json:"index"`
	Phase string `json:"phase,omitempty"`
	// Harness resolved at launch.
	Harness string `json:"harness,omitempty"`
	// Reason a Failed unit failed:
	// WorkspaceLost|HarnessUnavailable|JobFailed|Aborted|ResultTooLarge.
	Reason string `json:"reason,omitempty"`
	// Detail explains a failure for humans.
	Detail string `json:"detail,omitempty"`
	// Summary is the bounded digest once the unit settled.
	Summary *ResultSummary `json:"summary,omitempty"`
	// Result is the full unit result — Entry included — present once the
	// unit settled with one.
	Result *UnitResult `json:"result,omitempty"`
}

// EventWire is an SSE "event" payload: one relayed in-pod event, tagged with
// the unit it came from.
type EventWire struct {
	// Unit is the EvaluationUnit name.
	Unit string `json:"unit"`
	// Event is the relayed in-pod event, verbatim.
	Event Event `json:"event"`
}

// EvaluationStatusWire is the GET snapshot and the SSE "end" payload.
type EvaluationStatusWire struct {
	Name string `json:"name"`
	// Phase is Pending|Running|Complete|Failed.
	Phase     string `json:"phase,omitempty"`
	Submitter string `json:"submitter,omitempty"`
	// Unit counters.
	UnitsTotal    int `json:"unitsTotal"`
	UnitsComplete int `json:"unitsComplete"`
	UnitsFailed   int `json:"unitsFailed"`
	// Units are the per-child states, index-ordered.
	Units []UnitStatusWire `json:"units,omitempty"`
}
