// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package evaluation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// EventPrefix marks an event line on the runner's stdout; everything else the
// pod prints goes to stderr.
const EventPrefix = "EVOLVE-EVENT: "

// EventVersion is the current event schema version.
const EventVersion = 1

// Pod input environment: where the prepare container staged the unit spec (a
// UnitSpec JSON document) and the extracted workspace bundle.
const (
	EnvUnitFile  = "EVOLVE_UNIT_FILE"
	EnvBundleDir = "EVOLVE_BUNDLE_DIR"
)

// Event types. Patchy interprets only TypeResult and TypeFatal; every other
// type is progress, relayed verbatim to the monitoring client, which replays
// it onto its local reporter.
const (
	TypeUnitStarted     = "unit_started"
	TypeUnitSkipped     = "unit_skipped"
	TypeItemStarted     = "item_started"
	TypeItemDone        = "item_done"
	TypeBaselineStarted = "baseline_started"
	TypeBaselineDone    = "baseline_done"
	TypeUnitFinished    = "unit_finished"
	TypeWarn            = "warn"
	TypeResult          = "result"
	TypeFatal           = "fatal"
)

// UnitRef identifies the unit an event belongs to, mirroring the client's
// plan.UnitRef plus the plugin.
type UnitRef struct {
	Plugin string `json:"plugin,omitempty"`
	Skill  string `json:"skill"`
	// Key is the provider-qualified model id.
	Key string `json:"key"`
	// Kind is "triggers" or "evals".
	Kind string `json:"kind"`
}

// ItemMetrics mirrors the client's plan.ItemMetrics: the per-item figures a
// live dashboard renders. All fields optional.
type ItemMetrics struct {
	Hits                *int     `json:"hits,omitempty"`
	Runs                *int     `json:"runs,omitempty"`
	AvgRunSeconds       *float64 `json:"avgRunSeconds,omitempty"`
	InputTokens         *int     `json:"inputTokens,omitempty"`
	CacheReadTokens     *int     `json:"cacheReadTokens,omitempty"`
	CacheCreationTokens *int     `json:"cacheCreationTokens,omitempty"`
	OutputTokens        *int     `json:"outputTokens,omitempty"`
	CostUSD             *float64 `json:"costUSD,omitempty"`
	AssertPassed        *int     `json:"assertPassed,omitempty"`
	AssertTotal         *int     `json:"assertTotal,omitempty"`
}

// ItemEvent carries the per-item payloads. unit_started uses Total/Runs/Mode;
// item_started uses Index/Label/Runs; item_done (and the baseline pair) uses
// Index/Label/Status/Detail/Output/Metrics. Local workspace and log paths
// never appear — they are meaningless outside the pod.
type ItemEvent struct {
	Index int    `json:"index,omitempty"`
	Label string `json:"label,omitempty"`
	Runs  int    `json:"runs,omitempty"`
	// Total and Mode ride on unit_started: the unit's case count and its
	// run mode ("run" or "count-only").
	Total int    `json:"total,omitempty"`
	Mode  string `json:"mode,omitempty"`
	// Status is pass|fail|skip|error (item_done only).
	Status  string       `json:"status,omitempty"`
	Detail  string       `json:"detail,omitempty"`
	Output  string       `json:"output,omitempty"`
	Metrics *ItemMetrics `json:"metrics,omitempty"`
}

// UnitSummary mirrors the client's run.UnitSummary — the rollup reported when
// a unit finishes.
type UnitSummary struct {
	Executed      bool     `json:"executed"`
	Passed        int      `json:"passed"`
	Failed        int      `json:"failed"`
	Errored       int      `json:"errored"`
	Total         int      `json:"total"`
	AvgRunSeconds *float64 `json:"avgRunSeconds,omitempty"`
}

// Event is one EVOLVE-EVENT line: the pod's progress and result stream.
type Event struct {
	V    int      `json:"v"`
	Type string   `json:"type"`
	Unit *UnitRef `json:"unit,omitempty"`
	// Item carries the per-item payloads (see ItemEvent).
	Item *ItemEvent `json:"item,omitempty"`
	// Sum rides on unit_finished.
	Sum *UnitSummary `json:"sum,omitempty"`
	// Msg is the unit_skipped reason or the warn text.
	Msg string `json:"msg,omitempty"`
	// Result rides on type=result: the unit's outcome, exactly once per
	// successful pod, as the last event before exit.
	Result *UnitResult `json:"result,omitempty"`
	// Error rides on type=fatal: the pod could not produce a result.
	Error string `json:"error,omitempty"`
}

// Encode renders the event as one stdout line (prefix included, newline
// excluded).
func (e Event) Encode() (string, error) {
	e.V = EventVersion
	raw, err := json.Marshal(e)
	if err != nil {
		return "", fmt.Errorf("evaluation: encode event: %w", err)
	}
	return EventPrefix + string(raw), nil
}

// Decode recovers an event from one log line; ok is false for any line that
// is not an event. The prefix is found anywhere in the line, because
// Kubernetes log lines may carry timestamps or wrapping.
func Decode(line []byte) (Event, bool) {
	rest, found := bytes.CutPrefix(bytes.TrimSpace(line), []byte(EventPrefix))
	if !found {
		if i := strings.Index(string(line), EventPrefix); i >= 0 {
			rest = line[i+len(EventPrefix):]
		} else {
			return Event{}, false
		}
	}
	var e Event
	if err := json.Unmarshal(rest, &e); err != nil {
		return Event{}, false
	}
	if e.V != EventVersion || e.Type == "" {
		return Event{}, false
	}
	return e, true
}
