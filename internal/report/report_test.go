// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package report

import (
	"strings"
	"testing"
)

const validInvestigation = `---
exploitability:
  rating: high
  summary: reachable from the request path
likelihood:
  rating: medium
  summary: requires an authenticated caller
impact:
  rating: high
  summary: full table read
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

func TestParseInvestigation(t *testing.T) {
	inv, err := ParseInvestigation([]byte(validInvestigation))
	if err != nil {
		t.Fatalf("ParseInvestigation() error = %v", err)
	}
	if inv.Recommendation != RecommendRemediate || inv.Priority != "high" || inv.Severity != "high" {
		t.Errorf("parsed = %+v", inv)
	}
	if inv.Exploitability.Rating != "high" || inv.Likelihood.Rating != "medium" || inv.Impact.Rating != "high" {
		t.Errorf("dimensions = %s/%s/%s, want high/medium/high",
			inv.Exploitability.Rating, inv.Likelihood.Rating, inv.Impact.Rating)
	}
	if inv.Exploitability.Summary == "" {
		t.Error("exploitability summary lost")
	}
	if inv.Confidence == nil || *inv.Confidence != 0.85 {
		t.Errorf("Confidence = %v, want 0.85", inv.Confidence)
	}
	if inv.Model != "claude-sonnet-5" || inv.MaxTurns != 40 || inv.TokenBudget != 200000 {
		t.Errorf("remediation params = %q/%d/%d", inv.Model, inv.MaxTurns, inv.TokenBudget)
	}
	if !strings.Contains(inv.Body, "The finding is real.") {
		t.Errorf("Body = %q", inv.Body)
	}
}

func TestParseInvestigationIgnoreNeedsNoModel(t *testing.T) {
	src := `---
exploitability:
  rating: none
  summary: sink is constant
likelihood:
  rating: none
  summary: not reachable
impact:
  rating: low
  summary: nothing sensitive
recommendation: ignore
priority: low
severity: low
confidence: 0.95
breaking_change_available: false
---
False positive: the sink is constant.
`
	inv, err := ParseInvestigation([]byte(src))
	if err != nil {
		t.Fatalf("ParseInvestigation() error = %v", err)
	}
	if inv.Recommendation != RecommendIgnore {
		t.Errorf("Recommendation = %q", inv.Recommendation)
	}
}

func TestParseInvestigationErrors(t *testing.T) {
	replace := func(old, new string) string { return strings.Replace(validInvestigation, old, new, 1) }
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
		{"bad rating", replace("rating: medium", "rating: sometimes")},
		{"missing confidence", replace("confidence: 0.85\n", "")},
		{"confidence out of range", replace("confidence: 0.85", "confidence: 1.5")},
		{"remediate without model", replace("model: claude-sonnet-5\n", "")},
		{"remediate without max_turns", replace("max_turns: 40\n", "")},
		{"remediate without token_budget", replace("token_budget: 200000\n", "")},
		{"non-numeric confidence", replace("confidence: 0.85", "confidence: high")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseInvestigation([]byte(tt.src)); err == nil {
				t.Error("ParseInvestigation() error = nil, want error")
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
