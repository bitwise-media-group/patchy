// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package templates

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
)

var update = flag.Bool("update", false, "rewrite golden files")

func testFinding() *v1alpha1.Finding {
	return &v1alpha1.Finding{
		ObjectMeta: metav1.ObjectMeta{Namespace: "patchy", Name: "finding-abc123def0-1"},
		Spec: v1alpha1.FindingSpec{
			Source: "github-code-scanning",
			Repository: &v1alpha1.FindingRepository{
				Type: "git", URL: "https://github.com/acme/shop", Name: "acme/shop", DefaultBranch: "main",
			},
			Advisories:  []string{"CWE-79", "CVE-2026-1234"},
			RuleID:      "js/reflected-xss",
			Title:       "Reflected cross-site scripting",
			Description: "Directly writing user input to the page allows XSS.\n\nSanitize all user input.",
			Severity:    v1alpha1.LevelHigh,
			Alerts: []v1alpha1.Alert{
				{
					ID:  "7",
					URL: "https://github.com/acme/shop/security/code-scanning/7",
					Locations: []v1alpha1.Location{
						{Path: "src/render.js", StartLine: 42, EndLine: 44},
					},
				},
			},
		},
		Status: v1alpha1.FindingStatus{Phase: v1alpha1.PhaseOpened},
	}
}

// golden compares got with the named golden file, rewriting it under -update.
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("update golden %s: %v", path, err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (run with -update to create): %v", path, err)
	}
	if got != string(want) {
		t.Errorf("%s mismatch\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func TestGoldens(t *testing.T) {
	tests := []struct {
		name   string
		render func() (string, error)
	}{
		{"finding_issue.md", func() (string, error) { return RenderFindingIssue(testFinding()) }},
		{"pr_body.md", func() (string, error) { return PRBody(123, "remediation report here") }},
		{"prompt_investigate.md", func() (string, error) {
			return RenderInvestigatePrompt(InvestigatePrompt{
				IssuePath:          "/workspace/input/issue.md",
				ReportPath:         "/workspace/reports/investigation.md",
				AllowedModels:      []string{"claude-sonnet-5", "claude-opus-4-8"},
				MaxTurnsCeiling:    80,
				TokenBudgetCeiling: 400000,
			})
		}},
		{"prompt_remediate.md", func() (string, error) {
			return RenderRemediatePrompt(RemediatePrompt{
				IssuePath:         "/workspace/input/issue.md",
				InvestigationPath: "/workspace/input/investigation.md",
				ReportPath:        "/workspace/reports/remediation.md",
				CommitScriptPath:  "/workspace/commit.sh",
			})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.render()
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			golden(t, tt.name, got)
		})
	}
}

func TestFindingIssueTitle(t *testing.T) {
	got := FindingIssueTitle(testFinding())
	if want := "[github-code-scanning] CWE-79: Reflected cross-site scripting"; got != want {
		t.Errorf("FindingIssueTitle() = %q, want %q", got, want)
	}
}

func TestStageReportComment(t *testing.T) {
	got := RenderStageReportComment("investigation", 2, "report body")
	if !strings.Contains(got, "<!-- patchy:report investigation/2 -->") {
		t.Errorf("comment lacks the dedup marker:\n%s", got)
	}
	if !strings.Contains(got, "report body") {
		t.Errorf("comment lacks the report body:\n%s", got)
	}
}

func TestEnrichmentProjection(t *testing.T) {
	got := RenderEnrichmentProjection(v1alpha1.Enrichment{Enhancer: "cmdb", Markdown: "**Owners:** @octocat"})
	if !strings.Contains(got, "<!-- patchy:enrichment cmdb -->") || !strings.Contains(got, "@octocat") {
		t.Errorf("projection = %q", got)
	}
}
