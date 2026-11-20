// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package templates

import (
	"fmt"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
)

// FindingIssueTitle is the tracking-issue title projected from a Finding.
func FindingIssueTitle(f *v1alpha1.Finding) string {
	primary := ""
	if len(f.Spec.Advisories) > 0 {
		primary = f.Spec.Advisories[0]
	}
	return fmt.Sprintf("[%s] %s: %s", f.Spec.Source, primary, f.Spec.Title)
}

// RenderFindingIssue renders the tracking-issue body from the Finding. It is
// a human-facing projection only: the CR holds the state, and the body
// carries just a finding-name marker comment for debugging.
func RenderFindingIssue(f *v1alpha1.Finding) (string, error) {
	return render("finding_issue.md.tmpl", f)
}

// RenderStageReportComment renders an agent report as a tracking-issue
// comment, headed by the stage name and attempt so re-projection can dedup.
func RenderStageReportComment(stage string, attempt int32, report string) string {
	return fmt.Sprintf("<!-- patchy:report %s/%d -->\n## %s report (attempt %d)\n\n%s",
		stage, attempt, stage, attempt, report)
}

// EnrichmentMarker heads one enhancer's sticky tracking comment; the
// projection finds the comment to update in place by this prefix.
func EnrichmentMarker(enhancer string) string {
	return fmt.Sprintf("<!-- patchy:enrichment %s -->", enhancer)
}

// RenderEnrichmentProjection renders one enhancer enrichment as its sticky
// tracking-issue comment.
func RenderEnrichmentProjection(e v1alpha1.Enrichment) string {
	return EnrichmentMarker(e.Enhancer) + "\n" + e.Markdown
}
