// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package mirror

import "fmt"

// Event narrates engine progress. Events are stderr material (or NDJSON
// under -o json); typed results returned by engine methods are the data.
type Event struct {
	// Kind is "note" (progress), "warn" (survivable problem worth eyes)
	// or "stage" (a pipeline stage boundary).
	Kind string `json:"kind"`
	// Entry is the entry being worked, when applicable.
	Entry string `json:"entry,omitempty"`
	// Stage is the pipeline stage emitting the event.
	Stage string `json:"stage,omitempty"`
	// Message is the human-readable line.
	Message string `json:"message"`
}

// notef emits a note event.
func (e *Engine) notef(entry, stage, format string, args ...any) {
	e.onEvent(Event{Kind: "note", Entry: entry, Stage: stage, Message: fmt.Sprintf(format, args...)})
}

// warnf emits a warning event.
func (e *Engine) warnf(entry, stage, format string, args ...any) {
	e.onEvent(Event{Kind: "warn", Entry: entry, Stage: stage, Message: fmt.Sprintf(format, args...)})
}
