// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package agentresult converts agent envelope payloads into the CRD status
// shapes — the one place the float-bearing wire format meets the no-float
// structural schemas. Both job controllers (investigation, remediation) use
// it so cost/confidence formatting and size caps never drift apart.
package agentresult

import (
	"strconv"
	"unicode/utf8"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/envelope"
)

// Size caps (bytes) for CRD string fields.
const (
	maxReport = 65536
	maxDetail = 4096
)

// FromStage maps an envelope stage onto the CRD stage result.
func FromStage(st *envelope.Stage) *v1alpha1.StageResult {
	return &v1alpha1.StageResult{
		Outcome:   string(st.Outcome),
		Harness:   st.Harness,
		Model:     st.Model,
		SessionID: st.SessionID,
		NumTurns:  int32(st.NumTurns),
		Usage: v1alpha1.UsageSummary{
			InputTokens:         int64(st.Usage.InputTokens),
			OutputTokens:        int64(st.Usage.OutputTokens),
			CacheReadTokens:     int64(st.Usage.CacheReadTokens),
			CacheCreationTokens: int64(st.Usage.CacheCreationTokens),
			CostUSD:             FormatCost(st.Usage.CostUSD),
		},
		ElapsedMilliseconds: int64(st.ElapsedSeconds * 1000),
		Detail:              TruncateDetail(st.Detail),
	}
}

// Analysis maps one envelope analysis dimension; nil when unassessed.
func Analysis(a envelope.AnalysisResult) *v1alpha1.Analysis {
	if a.Rating == "" {
		return nil
	}
	return &v1alpha1.Analysis{Rating: v1alpha1.Rating(a.Rating), Summary: TruncateDetail(a.Summary)}
}

// FormatCost renders a float cost as the CRD's decimal string (6 fractional
// digits — micro-USD precision); empty when unreported.
func FormatCost(f float64) string {
	if f <= 0 {
		return ""
	}
	return strconv.FormatFloat(f, 'f', 6, 64)
}

// FormatConfidence renders confidence as the CRD's decimal string; empty
// when out of range.
func FormatConfidence(f float64) string {
	if f < 0 || f > 1 {
		return ""
	}
	return strconv.FormatFloat(f, 'f', 4, 64)
}

// TruncateReport caps a report markdown for a CRD field.
func TruncateReport(s string) string { return truncate(s, maxReport) }

// TruncateDetail caps a short detail/summary for a CRD field.
func TruncateDetail(s string) string { return truncate(s, maxDetail) }

// truncate caps s at limit bytes on a rune boundary (the API server rejects
// invalid UTF-8).
func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := s[:limit]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}
