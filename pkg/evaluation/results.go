// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package evaluation

import (
	"encoding/json"
	"unicode/utf8"
)

// MaxEntryBytes bounds UnitResult.Entry. The pod truncates evidence fields
// until the marshalled entry fits — the entry is stored in a ConfigMap under
// Kubernetes' ~1MiB object cap, and this bound leaves headroom for the
// object's own metadata after gzip.
const MaxEntryBytes = 900 << 10

// MaxDetailLen bounds human-facing detail strings on the wire (mirrors the
// CR status bound).
const MaxDetailLen = 4096

// MaxOutputLen bounds relayed item output snippets, so one chatty case
// cannot bloat the progress stream.
const MaxOutputLen = 16 << 10

// TokenUsage is a unit's token and cost accounting, summed over its agent
// runs. Cost stays a float on the wire; CR statuses render it as a decimal
// string.
type TokenUsage struct {
	InputTokens         int64   `json:"inputTokens,omitempty"`
	OutputTokens        int64   `json:"outputTokens,omitempty"`
	CacheReadTokens     int64   `json:"cacheReadTokens,omitempty"`
	CacheCreationTokens int64   `json:"cacheCreationTokens,omitempty"`
	CostUSD             float64 `json:"costUSD,omitempty"`
}

// CaseStatus is one case's graded outcome, compact enough for CR status.
type CaseStatus struct {
	// ID is the trigger query or eval id.
	ID string `json:"id"`
	// Passed reports the graded outcome.
	Passed bool `json:"passed"`
}

// ResultSummary is the typed, bounded portion of a unit result — everything
// patchy stamps into the EvaluationUnit status.
type ResultSummary struct {
	CasesPassed  int `json:"casesPassed"`
	CasesFailed  int `json:"casesFailed"`
	CasesErrored int `json:"casesErrored"`
	// Cases lists per-case outcomes; the pod bounds the list (the CR caps
	// it at 256 entries).
	Cases []CaseStatus `json:"cases,omitempty"`
	// TokenUsage summed over the unit's agent runs.
	TokenUsage TokenUsage `json:"tokenUsage,omitempty"`
	// ElapsedMS is the unit's wall-clock duration.
	ElapsedMS int64 `json:"elapsedMS,omitempty"`
	// Outcome is "ok" for a completed unit (graded failures included) or a
	// short failure word ("error", "timeout") otherwise.
	Outcome string `json:"outcome"`
}

// UnitResult is the type=result payload: the unit's outcome plus the
// finished results entry.
type UnitResult struct {
	// Tier (1|2), Model, and Harness echo what actually ran, so the result
	// is self-contained.
	Tier    int    `json:"tier"`
	Model   string `json:"model"`
	Harness string `json:"harness"`
	// Failed reports whether any executed case failed — the client's
	// --strict signal, not a scheduling failure.
	Failed bool `json:"failed"`
	// Summary is the bounded, typed digest patchy stores in CR status.
	Summary ResultSummary `json:"summary"`
	// Entry is the finished results entry (the client's TriggerEntry or
	// EvalEntry, schema 5), OPAQUE to patchy: stored whole in the results
	// ConfigMap and handed back to the client, which merges it into its
	// local results file. At most MaxEntryBytes.
	Entry json.RawMessage `json:"entry,omitempty"`
}

// Truncate clips s to at most max bytes on a rune boundary, appending an
// ellipsis when it clipped. Used by the pod to bound detail/output strings
// and evidence fields until an entry fits MaxEntryBytes.
func Truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	const ellipsis = "…"
	cut := max - len(ellipsis)
	if cut < 0 {
		cut = 0
	}
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + ellipsis
}
