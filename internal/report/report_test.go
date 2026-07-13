// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package report

import (
	"strings"
	"testing"
)

const validClassification = `---
recommendation: remediate
priority: high
severity: high
confidence: 0.85
breaking_change_available: false
model: claude-sonnet-5
max_turns: 40
token_budget: 200000
---

## Analysis

The finding is real.
`

func TestParseClassification(t *testing.T) {
	c, err := ParseClassification([]byte(validClassification))
	if err != nil {
		t.Fatalf("ParseClassification() error = %v", err)
	}
	if c.Recommendation != RecommendRemediate || c.Priority != "high" || c.Severity != "high" {
		t.Errorf("parsed = %+v", c)
	}
	if c.Confidence == nil || *c.Confidence != 0.85 {
		t.Errorf("Confidence = %v, want 0.85", c.Confidence)
	}
	if c.Model != "claude-sonnet-5" || c.MaxTurns != 40 || c.TokenBudget != 200000 {
		t.Errorf("remediation params = %q/%d/%d", c.Model, c.MaxTurns, c.TokenBudget)
	}
	if !strings.Contains(c.Body, "The finding is real.") {
		t.Errorf("Body = %q", c.Body)
	}
}

func TestParseClassificationIgnoreNeedsNoModel(t *testing.T) {
	src := `---
recommendation: ignore
priority: low
severity: low
confidence: 0.95
breaking_change_available: false
---
False positive: the sink is constant.
`
	c, err := ParseClassification([]byte(src))
	if err != nil {
		t.Fatalf("ParseClassification() error = %v", err)
	}
	if c.Recommendation != RecommendIgnore {
		t.Errorf("Recommendation = %q", c.Recommendation)
	}
}

func TestParseClassificationErrors(t *testing.T) {
	replace := func(old, new string) string { return strings.Replace(validClassification, old, new, 1) }
	tests := []struct {
		name string
		src  string
	}{
		{"no frontmatter", "just markdown"},
		{"unterminated", "---\nrecommendation: ignore\n"},
		{"unknown key", replace("model:", "mdoel:")},
		{"bad recommendation", replace("recommendation: remediate", "recommendation: dismiss")},
		{"bad priority", replace("priority: high", "priority: urgent")},
		{"bad severity", replace("severity: high", "severity: severe")},
		{"missing confidence", replace("confidence: 0.85\n", "")},
		{"confidence out of range", replace("confidence: 0.85", "confidence: 1.5")},
		{"remediate without model", replace("model: claude-sonnet-5\n", "")},
		{"remediate without max_turns", replace("max_turns: 40\n", "")},
		{"remediate without token_budget", replace("token_budget: 200000\n", "")},
		{"non-numeric confidence", replace("confidence: 0.85", "confidence: high")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseClassification([]byte(tt.src)); err == nil {
				t.Error("ParseClassification() error = nil, want error")
			}
		})
	}
}

const validRemediation = `---
success: true
confidence: 0.9
---
Escaped the sink; tests pass.
`

func TestParseRemediation(t *testing.T) {
	r, err := ParseRemediation([]byte(validRemediation))
	if err != nil {
		t.Fatalf("ParseRemediation() error = %v", err)
	}
	if r.Success == nil || !*r.Success {
		t.Errorf("Success = %v, want true", r.Success)
	}
	if r.Confidence == nil || *r.Confidence != 0.9 {
		t.Errorf("Confidence = %v, want 0.9", r.Confidence)
	}
	if !strings.Contains(r.Body, "tests pass") {
		t.Errorf("Body = %q", r.Body)
	}
}

func TestParseRemediationFalseIsNotAbsent(t *testing.T) {
	r, err := ParseRemediation([]byte("---\nsuccess: false\nconfidence: 0.2\n---\ncould not fix\n"))
	if err != nil {
		t.Fatalf("ParseRemediation() error = %v", err)
	}
	if r.Success == nil || *r.Success {
		t.Errorf("Success = %v, want explicit false", r.Success)
	}
}

func TestParseRemediationErrors(t *testing.T) {
	tests := []struct {
		name string
		src  string
	}{
		{"missing success", "---\nconfidence: 0.5\n---\nbody"},
		{"missing confidence", "---\nsuccess: true\n---\nbody"},
		{"confidence out of range", "---\nsuccess: true\nconfidence: -0.1\n---\nbody"},
		{"unknown key", "---\nsuccess: true\nconfidence: 0.5\nnotes: hi\n---\nbody"},
		{"no frontmatter", "body only"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseRemediation([]byte(tt.src)); err == nil {
				t.Error("ParseRemediation() error = nil, want error")
			}
		})
	}
}
