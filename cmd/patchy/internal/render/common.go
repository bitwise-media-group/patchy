// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package render

import (
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/printer"
	"github.com/bitwise-media-group/patchy/internal/report"
)

// Rating renders one investigation analysis dimension: the grade, plus the
// agent's one-line justification when it gave one.
func Rating(a *v1alpha1.Analysis) string {
	if a == nil {
		return ""
	}
	if a.Summary == "" {
		return string(a.Rating)
	}
	return fmt.Sprintf("%s — %s", a.Rating, a.Summary)
}

// Stage adds the agent accounting both run kinds carry.
func Stage(d *printer.Doc, st *v1alpha1.StageResult, now time.Time) {
	if st == nil {
		return
	}
	d.Section("Agent run").
		Field("Outcome", Outcome(st)).
		Field("Harness", st.Harness).
		Field("Model", st.Model).
		Field("Session", st.SessionID).
		Fieldf("Turns", "%d", st.NumTurns).
		Field("Started", Timestamp(st.StartedAt, now)).
		Field("Finished", Timestamp(st.FinishedAt, now)).
		Field("Usage", Usage(st.Usage))
}

// Outcome reports how an agent run ended. Worth surfacing next to the report
// because it explains an empty one far better than the empty report does.
func Outcome(st *v1alpha1.StageResult) string {
	if st == nil {
		return ""
	}
	if st.Detail == "" {
		return st.Outcome
	}
	return fmt.Sprintf("%s — %s", st.Outcome, st.Detail)
}

// Cost renders a run's spend on one line.
func Cost(st *v1alpha1.StageResult) string {
	if st == nil || st.Usage.CostUSD == "" {
		return ""
	}
	return fmt.Sprintf("$%s on %s (%d turns)", st.Usage.CostUSD, st.Model, st.NumTurns)
}

// Usage renders the token and cost accounting.
func Usage(u v1alpha1.UsageSummary) string {
	if u == (v1alpha1.UsageSummary{}) {
		return ""
	}
	s := fmt.Sprintf("%d in / %d out tokens", u.InputTokens, u.OutputTokens)
	if u.CacheReadTokens > 0 || u.CacheCreationTokens > 0 {
		s += fmt.Sprintf(" (%d cache read, %d cache write)", u.CacheReadTokens, u.CacheCreationTokens)
	}
	if u.CostUSD != "" {
		s += fmt.Sprintf(", $%s", u.CostUSD)
	}
	return s
}

// Body is the report as a human should read it: the machine frontmatter is a
// contract between stages, not prose, so it comes off unless asked for.
func Body(rep string, raw bool) string {
	if raw {
		return rep
	}
	return report.StripFrontmatter(rep)
}

// Age renders a duration the way kubectl does: one or two units, largest first.
func Age(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}

// Timestamp renders an absolute time with its age. A detail view wants both:
// "when" and "how long ago" answer different questions.
func Timestamp(t *metav1.Time, now time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return fmt.Sprintf("%s (%s ago)", t.Format(time.RFC3339), Age(t.Time, now))
}

// Dash renders an empty value inside a composed line, where omitting it would
// leave a confusing gap.
func Dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
