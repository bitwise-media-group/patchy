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

// testSCCFinding is a Security Command Center finding with every optional
// block populated.
func testSCCFinding() SCCFinding {
	return SCCFinding{
		Name:        "organizations/1234567890/sources/555/findings/abc123",
		Category:    "PUBLIC_BUCKET_ACL",
		Class:       "MISCONFIGURATION",
		Severity:    "high",
		Description: "The bucket is publicly readable.",
		NextSteps:   "Remove allUsers from the bucket IAM policy.",
		Resource: SCCResource{
			Name:        "//storage.googleapis.com/projects/acme-prod/buckets/acme-artifacts",
			DisplayName: "acme-artifacts",
			Type:        "google.cloud.storage.Bucket",
			Project:     "projects/acme-prod",
			Location:    "europe-west2",
			Service:     "storage.googleapis.com",
		},
		CVE:             "CVE-2026-1234",
		CVSS:            "7.5",
		MitreTactic:     "IMPACT",
		MitreTechniques: []string{"T1485", "T1486"},
		Compliances: []SCCCompliance{
			{Standard: "cis", Version: "1.3", IDs: []string{"5.1", "5.2"}},
		},
		Properties: []SCCKV{
			{Key: "ExceptionInstructions", Value: "Add the mark"},
			{Key: "Recommendation", Value: "Restrict access"},
		},
		Marks: []SCCKV{
			{Key: "scm-repository-name", Value: "infra-prod"},
			{Key: "scm-repository-org", Value: "acme"},
		},
		DetectedAt:  "2026-07-26T09:00:00Z",
		ExternalURI: "https://console.cloud.google.com/security/command-center/findings?f=abc123",
	}
}

// testWizIssue is a Wiz issue with every optional block populated.
func testWizIssue() WizIssue {
	return WizIssue{
		ID:             "f2f5d3b8-a663-4c1b-b7f3-8f7f0c8a0001",
		ControlName:    "Bucket accessible to the public",
		ControlID:      "wc-id-1234",
		Severity:       "high",
		Status:         "OPEN",
		Description:    "The bucket allows public read access.",
		Recommendation: "Remove allUsers from the bucket IAM policy.",
		Entity: WizEntity{
			Name:             "//storage.googleapis.com/acme-artifacts",
			DisplayName:      "acme-artifacts",
			Type:             "storage#bucket",
			Platform:         "GCP",
			Subscription:     "acme-prod",
			SubscriptionName: "Acme Production",
			Region:           "europe-west2",
			ConsoleURL:       "https://console.cloud.google.com/storage/browser/acme-artifacts",
			Tags: []WizKV{
				{Key: "env", Value: "prod"},
				{Key: "team", Value: "storage"},
			},
		},
		Projects:  "acme-prod",
		CreatedAt: "2026-07-26T09:00:00Z",
		URL:       "https://app.wiz.io/issues#~(issue~'f2f5d3b8')",
	}
}

// testWizThreat is a Wiz Defend threat with every optional block populated.
func testWizThreat() WizThreat {
	return WizThreat{
		ID:          "t-0001",
		Name:        "Suspicious service account key usage",
		RuleName:    "Unusual key usage",
		RuleID:      "dr-5678",
		Severity:    "critical",
		Status:      "OPEN",
		Description: "A service account key was used from an unusual location.",
		Entity: WizEntity{
			Name:         "//compute.googleapis.com/projects/acme-prod/zones/europe-west2-a/instances/build-vm",
			DisplayName:  "build-vm",
			Type:         "compute#instance",
			Platform:     "GCP",
			Subscription: "acme-prod",
			Region:       "europe-west2-a",
		},
		Actors:          []string{"ci-deployer@acme-prod.iam (service_account)"},
		MitreTactics:    []string{"TA0001"},
		MitreTechniques: []string{"T1078"},
		Detections:      2,
		CloudAccounts:   []string{"acme-prod"},
		CreatedAt:       "2026-07-26T10:00:00Z",
		URL:             "https://app.wiz.io/threats#~(threat~'t-0001')",
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
				IssuePath:         "/workspace/input/issue.md",
				ReportPath:        "/workspace/reports/investigation.md",
				AllowedModels:     []string{"claude-sonnet-5", "claude-opus-5"},
				AutoMaxTurns:      80,
				AutoTokenBudget:   400000,
				ManualMaxTurns:    240,
				ManualTokenBudget: 1200000,
			})
		}},
		// The calibrated variant: the feedback loop's rendered form, with a
		// history skewing hard in both directions (turns badly under-predicted,
		// tokens over-predicted) so the sign handling is pinned.
		{"prompt_investigate_calibrated.md", func() (string, error) {
			return RenderInvestigatePrompt(InvestigatePrompt{
				IssuePath:         "/workspace/input/issue.md",
				ReportPath:        "/workspace/reports/investigation.md",
				AllowedModels:     []string{"claude-sonnet-5", "claude-opus-5"},
				AutoMaxTurns:      80,
				AutoTokenBudget:   400000,
				ManualMaxTurns:    240,
				ManualTokenBudget: 1200000,
				Calibration: &Calibration{
					Scope: "acme/orders", Runs: 12,
					AvgPredictedTurns: 12, AvgActualTurns: 34,
					AvgPredictedOutputTokens: 50000, AvgActualOutputTokens: 41000,
				},
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
		// A cloud finding's description, carrying every optional block, so the
		// goldens pin the whole shape rather than the happy subset.
		{"finding_gcp_scc.md", func() (string, error) { return RenderSCCDescription(testSCCFinding()) }},
		// The same finding as a bare misconfiguration: no CVE, no ATT&CK, no
		// compliance, no marks. Every one of those sections must vanish
		// cleanly rather than leave an empty heading behind.
		{"finding_gcp_scc_minimal.md", func() (string, error) {
			return RenderSCCDescription(SCCFinding{
				Name:        "organizations/1234567890/sources/555/findings/abc123",
				Category:    "PUBLIC_BUCKET_ACL",
				Class:       "MISCONFIGURATION",
				Severity:    "high",
				Description: "The bucket is publicly readable.",
				Resource: SCCResource{
					Name: "//storage.googleapis.com/projects/acme-prod/buckets/acme-artifacts",
					Type: "google.cloud.storage.Bucket",
				},
			})
		}},
		// A Wiz issue with every optional block, pinning the whole shape.
		{"finding_wiz_issue.md", func() (string, error) { return RenderWizIssueDescription(testWizIssue()) }},
		// The minimal variant: no recommendation, no tags, no console link —
		// each section must vanish cleanly.
		{"finding_wiz_issue_minimal.md", func() (string, error) {
			return RenderWizIssueDescription(WizIssue{
				ID:          "f2f5d3b8-a663-4c1b-b7f3-8f7f0c8a0001",
				ControlName: "Bucket accessible to the public",
				Severity:    "high",
				Description: "The bucket allows public read access.",
				Entity: WizEntity{
					Name: "//storage.googleapis.com/acme-artifacts",
					Type: "storage#bucket",
				},
			})
		}},
		{"finding_wiz_threat.md", func() (string, error) { return RenderWizThreatDescription(testWizThreat()) }},
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
