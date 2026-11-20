// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package labels

import (
	"slices"
	"strings"
)

// MaxLen is GitHub's label-name length cap; Render never emits a longer name.
const MaxLen = 50

// sep joins a label key and its value ("security-finding: opened"). Parse
// also accepts the value without the space.
const sep = ": "

// State is the security-finding label value: the kebab-case rendering of the
// Finding's phase.
type State string

// Level is a severity or priority value.
type Level string

// The severity/priority scale.
const (
	LevelLow      Level = "low"
	LevelMedium   Level = "medium"
	LevelHigh     Level = "high"
	LevelCritical Level = "critical"
)

// Recommendation is the investigation's verdict, in the same vocabulary the
// agent writes in its report frontmatter (internal/report.Recommendation).
type Recommendation string

// The investigation verdicts.
const (
	RecommendationRemediate Recommendation = "remediate"
	RecommendationIgnore    Recommendation = "ignore"
	RecommendationManual    Recommendation = "manual"
)

// Label keys. Every patchy label is "<key>: <value>"; the multi-valued
// advisory key emits one label per value. This is the full human-facing
// vocabulary the issue projection renders. The Finding CR holds the state, so
// no machine or usage labels exist.
const (
	KeySource         = "security-source"
	KeyAdvisory       = "security-advisory"
	KeyFinding        = "security-finding"
	KeySeverity       = "security-severity"
	KeyPriority       = "security-priority"
	KeyRecommendation = "security-recommendation"
	KeyContext        = "security-context"
)

// Set is the typed view of one issue's patchy labels. Zero fields render no
// label.
type Set struct {
	Source         string
	Advisories     []string
	Finding        State
	Severity       Level
	Priority       Level
	Recommendation Recommendation
	// Context carries enhancer attributes, one "security-context: k=v" label
	// per entry.
	Context map[string]string
}

// Parse builds a Set from raw GitHub label names. It is tolerant: labels
// without a "key:" form, unknown keys, and security-context values with no
// "=" are ignored, and other values are kept as written without validation.
// Issues carry human labels too, and a foreign security-* label must never
// wedge the pipeline.
func Parse(names []string) Set {
	var s Set
	for _, name := range names {
		key, value, ok := strings.Cut(name, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case KeySource:
			s.Source = value
		case KeyAdvisory:
			s.Advisories = append(s.Advisories, value)
		case KeyFinding:
			s.Finding = State(value)
		case KeySeverity:
			s.Severity = Level(value)
		case KeyPriority:
			s.Priority = Level(value)
		case KeyRecommendation:
			s.Recommendation = Recommendation(value)
		case KeyContext:
			k, v, ok := strings.Cut(value, "=")
			if !ok || k == "" {
				continue
			}
			if s.Context == nil {
				s.Context = map[string]string{}
			}
			s.Context[k] = v
		}
	}
	slices.Sort(s.Advisories)
	return s
}

// Name renders a single label name ("key: value"), enforcing MaxLen. It is
// the form used for GitHub label filters and search queries.
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
	add(KeyFinding, string(s.Finding))
	add(KeySeverity, string(s.Severity))
	add(KeyPriority, string(s.Priority))
	add(KeyRecommendation, string(s.Recommendation))
	for _, k := range sortedKeys(s.Context) {
		if name := contextName(k, s.Context[k]); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// contextName renders one attribute label ("security-context: k=v") within
// MaxLen by truncating the value; an attribute whose key alone leaves no
// room for a value is excluded (blind truncation would corrupt the key).
func contextName(k, v string) string {
	budget := MaxLen - len(KeyContext) - len(sep) - len(k) - len("=")
	if budget <= 0 {
		return ""
	}
	if len(v) > budget {
		v = v[:budget]
	}
	return KeyContext + sep + k + "=" + v
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

func sorted(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	return out
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
