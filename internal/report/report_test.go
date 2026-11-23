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

func TestParseInvestigationDismissReason(t *testing.T) {
	ignore := strings.NewReplacer(
		"recommendation: remediate", "recommendation: ignore",
		"model: claude-sonnet-5\n", "", "max_turns: 40\n", "", "token_budget: 200000\n", "",
	).Replace(validInvestigation)
	withReason := func(src, reason string) string {
		return strings.Replace(src, "breaking_change_available: false\n",
			"breaking_change_available: false\ndismiss_reason: "+reason+"\n", 1)
	}
	tests := []struct {
		name    string
		src     string
		want    DismissReason
		wantErr bool
	}{
		{name: "absent", src: ignore},
		{name: "used in tests", src: withReason(ignore, "used_in_tests"), want: DismissUsedInTests},
		{name: "wont fix", src: withReason(ignore, "wont_fix"), want: DismissWontFix},
		{name: "false positive", src: withReason(ignore, "false_positive"), want: DismissFalsePositive},
		{name: "unknown reason", src: withReason(ignore, "revoked"), wantErr: true},
		{name: "not an ignore", src: withReason(validInvestigation, "used_in_tests"), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inv, err := ParseInvestigation([]byte(tt.src))
			if tt.wantErr {
				if err == nil {
					t.Error("ParseInvestigation() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseInvestigation() error = %v", err)
			}
			if inv.DismissReason != tt.want {
				t.Errorf("DismissReason = %q, want %q", inv.DismissReason, tt.want)
			}
		})
	}
}

func TestParseInvestigationRepairsUnquotedColonSummary(t *testing.T) {
	// Models write summaries as plain prose; "CWE-614: ..." is invalid as a
	// plain YAML scalar. The parser must quote-and-retry rather than fail
	// the whole run.
	src := strings.Replace(validInvestigation,
		"summary: reachable from the request path",
		"summary: CWE-614: the session cookie is set without the Secure attribute", 1)
	inv, err := ParseInvestigation([]byte(src))
	if err != nil {
		t.Fatalf("ParseInvestigation() error = %v", err)
	}
	if want := "CWE-614: the session cookie is set without the Secure attribute"; inv.Exploitability.Summary != want {
		t.Errorf("Exploitability.Summary = %q, want %q", inv.Exploitability.Summary, want)
	}
	// The untouched siblings survive the repair pass unchanged.
	if inv.Likelihood.Summary != "requires an authenticated caller" {
		t.Errorf("Likelihood.Summary = %q", inv.Likelihood.Summary)
	}
	if inv.Recommendation != RecommendRemediate || *inv.Confidence != 0.85 {
		t.Errorf("parsed = %+v", inv)
	}
}

func TestParseInvestigationRepairEscapesQuotes(t *testing.T) {
	src := strings.Replace(validInvestigation,
		"summary: requires an authenticated caller",
		`summary: attacker needs the "admin: true" claim`, 1)
	inv, err := ParseInvestigation([]byte(src))
	if err != nil {
		t.Fatalf("ParseInvestigation() error = %v", err)
	}
	if want := `attacker needs the "admin: true" claim`; inv.Likelihood.Summary != want {
		t.Errorf("Likelihood.Summary = %q, want %q", inv.Likelihood.Summary, want)
	}
}

func TestParseInvestigationRepairDoesNotMaskOtherErrors(t *testing.T) {
	// A broken summary plus a genuinely bad frontmatter: the repair retry
	// must not swallow the failure, and the original error is reported.
	src := strings.Replace(validInvestigation,
		"summary: full table read",
		"summary: impact: full table read", 1)
	src = strings.Replace(src, "model:", "mdoel:", 1)
	if _, err := ParseInvestigation([]byte(src)); err == nil {
		t.Error("ParseInvestigation() error = nil, want error")
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

func TestStripFrontmatter(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{"fenced report", "---\nconfidence: 0.9\n---\n\n## Analysis\n\nbody", "## Analysis\n\nbody"},
		{"body only", "## Analysis\n\nbody", "## Analysis\n\nbody"},
		{"unterminated fence", "---\nconfidence: 0.9\nbody", "---\nconfidence: 0.9\nbody"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StripFrontmatter(tt.src); got != tt.want {
				t.Errorf("StripFrontmatter() = %q, want %q", got, tt.want)
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
