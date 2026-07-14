// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package labels

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// MaxLen is GitHub's label-name length cap; Render never emits a longer name.
const MaxLen = 50

// sep joins a label key and its value ("security-finding: opened"). Parse
// also accepts the value without the space.
const sep = ": "

// sessionLen is how much of a session identifier fits a label; the full id
// lives in the report comment frontmatter.
const sessionLen = 8

// State is the security-finding lifecycle value.
type State string

// The security-finding states, in pipeline order. Remediated and Attempted
// are terminal; Attempted marks a finding handed back to humans after
// remediation failed or was exhausted.
const (
	StateOpened          State = "opened"
	StateContextEnhanced State = "context-enhanced"
	StateClassifying     State = "classifying"
	StateClassified      State = "classified"
	StateInReview        State = "in-review"
	StateRemediated      State = "remediated"
	StateAttempted       State = "attempted"
)

// Accumulation is the security-accumulation value gating the 1-hour window.
type Accumulation string

// AccumulationOpen collects further alerts into the issue;
// AccumulationComplete releases it to the remediation controller.
const (
	AccumulationOpen     Accumulation = "open"
	AccumulationComplete Accumulation = "complete"
)

// Level is a severity or priority value.
type Level string

// The severity/priority scale. GHAS emits critical, so the scale is
// four-valued even where DESIGN.md prose says three.
const (
	LevelLow      Level = "low"
	LevelMedium   Level = "medium"
	LevelHigh     Level = "high"
	LevelCritical Level = "critical"
)

// Recommendation is the classifier's verdict — the same vocabulary the
// agent writes in its report frontmatter (internal/report.Recommendation).
type Recommendation string

// The classifier verdicts.
const (
	RecommendationRemediate Recommendation = "remediate"
	RecommendationIgnore    Recommendation = "ignore"
	RecommendationManual    Recommendation = "manual"
)

// Label keys. Every patchy label is "<key>: <value>"; multi-valued keys
// (advisory, alert) emit one label per value.
const (
	KeySource         = "security-source"
	KeyAdvisory       = "security-advisory"
	KeyAlert          = "security-alert"
	KeyFinding        = "security-finding"
	KeyAccumulation   = "security-accumulation"
	KeySeverity       = "security-severity"
	KeyPriority       = "security-priority"
	KeyRecommendation = "security-recommendation"
	KeyConfidence     = "security-recommendation-confidence"
	KeyTokenBudget    = "security-token-budget"
	KeyMaxTurns       = "security-max-turns"
	KeyAttempts       = "security-attempts"
	KeyClassifier     = "security-classifier"
	KeyRemediator     = "security-remediator"
)

// Usage-label key prefixes; the stage prefix is followed by the metric name
// (input-tokens, output-tokens, turns, cost, session, elapsed).
const (
	classificationPrefix = "security-classification-"
	remediationPrefix    = "security-remediation-"
)

// Usage is the per-stage agent accounting surfaced as labels.
type Usage struct {
	InputTokens  int
	OutputTokens int
	Turns        int
	CostUSD      float64
	Session      string
	Elapsed      time.Duration
}

// Set is the typed view of one issue's patchy labels. Zero fields render no
// label; pointer fields distinguish absent from zero.
type Set struct {
	Source         string
	Advisories     []string
	Alerts         []int
	Finding        State
	Accumulation   Accumulation
	Severity       Level
	Priority       Level
	Recommendation Recommendation
	Confidence     *float64
	TokenBudget    int
	MaxTurns       int
	Attempts       int
	Classifier     string
	Remediator     string
	Classification *Usage
	Remediation    *Usage
}

// Parse builds a Set from raw GitHub label names. It is tolerant: labels
// without the security- prefix, unknown keys, and unparseable values are
// ignored — issues carry human labels too, and a foreign security-* label
// must never wedge the pipeline.
func Parse(names []string) Set {
	var s Set
	for _, name := range names {
		key, value, ok := strings.Cut(name, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		s.parseField(strings.TrimSpace(key), value)
	}
	slices.Sort(s.Advisories)
	slices.Sort(s.Alerts)
	return s
}

func (s *Set) parseField(key, value string) {
	if s.parseUsageField(key, value) {
		return
	}
	switch key {
	case KeySource:
		s.Source = value
	case KeyAdvisory:
		s.Advisories = append(s.Advisories, value)
	case KeyAlert:
		if n, err := strconv.Atoi(value); err == nil {
			s.Alerts = append(s.Alerts, n)
		}
	case KeyFinding:
		s.Finding = State(value)
	case KeyAccumulation:
		s.Accumulation = Accumulation(value)
	case KeySeverity:
		s.Severity = Level(value)
	case KeyPriority:
		s.Priority = Level(value)
	case KeyRecommendation:
		s.Recommendation = Recommendation(value)
	case KeyConfidence:
		if f, err := strconv.ParseFloat(value, 64); err == nil {
			s.Confidence = &f
		}
	case KeyTokenBudget:
		s.TokenBudget, _ = strconv.Atoi(value)
	case KeyMaxTurns:
		s.MaxTurns, _ = strconv.Atoi(value)
	case KeyAttempts:
		s.Attempts, _ = strconv.Atoi(value)
	case KeyClassifier:
		s.Classifier = value
	case KeyRemediator:
		s.Remediator = value
	}
}

// parseUsageField consumes the per-stage usage keys, reporting whether the
// key belonged to a usage prefix.
func (s *Set) parseUsageField(key, value string) bool {
	switch {
	case strings.HasPrefix(key, classificationPrefix):
		if s.Classification == nil {
			s.Classification = &Usage{}
		}
		parseUsageMetric(s.Classification, strings.TrimPrefix(key, classificationPrefix), value)
		return true
	case strings.HasPrefix(key, remediationPrefix):
		if s.Remediation == nil {
			s.Remediation = &Usage{}
		}
		parseUsageMetric(s.Remediation, strings.TrimPrefix(key, remediationPrefix), value)
		return true
	}
	return false
}

func parseUsageMetric(u *Usage, metric, value string) {
	switch metric {
	case "input-tokens":
		u.InputTokens, _ = strconv.Atoi(value)
	case "output-tokens":
		u.OutputTokens, _ = strconv.Atoi(value)
	case "turns":
		u.Turns, _ = strconv.Atoi(value)
	case "cost":
		u.CostUSD, _ = strconv.ParseFloat(value, 64)
	case "session":
		u.Session = value
	case "elapsed":
		if d, err := time.ParseDuration(value); err == nil {
			u.Elapsed = d
		}
	}
}

// Name renders a single label name ("key: value"), enforcing MaxLen — the
// form used for GitHub label filters and search queries.
func Name(key, value string) string {
	name := key + sep + value
	if len(name) > MaxLen {
		name = name[:MaxLen]
	}
	return name
}

// Render emits the label names for the set, deterministically ordered, each
// within MaxLen. Zero fields emit nothing.
func (s Set) Render() []string {
	var out []string
	add := func(key, value string) {
		if value == "" {
			return
		}
		out = append(out, Name(key, value))
	}

	add(KeySource, s.Source)
	for _, a := range sorted(s.Advisories) {
		add(KeyAdvisory, a)
	}
	for _, n := range sortedInts(s.Alerts) {
		add(KeyAlert, strconv.Itoa(n))
	}
	add(KeyFinding, string(s.Finding))
	add(KeyAccumulation, string(s.Accumulation))
	add(KeySeverity, string(s.Severity))
	add(KeyPriority, string(s.Priority))
	add(KeyRecommendation, string(s.Recommendation))
	if s.Confidence != nil {
		add(KeyConfidence, formatFloat(*s.Confidence))
	}
	if s.TokenBudget > 0 {
		add(KeyTokenBudget, strconv.Itoa(s.TokenBudget))
	}
	if s.MaxTurns > 0 {
		add(KeyMaxTurns, strconv.Itoa(s.MaxTurns))
	}
	if s.Attempts > 0 {
		add(KeyAttempts, strconv.Itoa(s.Attempts))
	}
	add(KeyClassifier, s.Classifier)
	add(KeyRemediator, s.Remediator)
	renderUsage(add, classificationPrefix, s.Classification)
	renderUsage(add, remediationPrefix, s.Remediation)
	return out
}

func renderUsage(add func(key, value string), prefix string, u *Usage) {
	if u == nil {
		return
	}
	add(prefix+"input-tokens", strconv.Itoa(u.InputTokens))
	add(prefix+"output-tokens", strconv.Itoa(u.OutputTokens))
	add(prefix+"turns", strconv.Itoa(u.Turns))
	add(prefix+"cost", formatFloat(u.CostUSD))
	if u.Session != "" {
		add(prefix+"session", truncate(u.Session, sessionLen))
	}
	add(prefix+"elapsed", fmt.Sprintf("%.1fs", u.Elapsed.Seconds()))
}

// Diff returns the label names to add to and remove from an issue to move it
// from prev to next. Labels outside the security- namespace are never touched.
func Diff(prev, next Set) (add, remove []string) {
	before := prev.Render()
	after := next.Render()
	for _, name := range after {
		if !slices.Contains(before, name) {
			add = append(add, name)
		}
	}
	for _, name := range before {
		if !slices.Contains(after, name) {
			remove = append(remove, name)
		}
	}
	return add, remove
}

// formatFloat renders confidence/cost values compactly: at most four decimal
// places, trailing zeros trimmed ("0.75", "0.4123").
func formatFloat(f float64) string {
	s := strconv.FormatFloat(f, 'f', 4, 64)
	s = strings.TrimRight(s, "0")
	return strings.TrimRight(s, ".")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func sorted(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	return out
}

func sortedInts(s []int) []int {
	out := slices.Clone(s)
	slices.Sort(out)
	return out
}
