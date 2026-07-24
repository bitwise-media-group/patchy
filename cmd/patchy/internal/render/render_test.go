// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package render_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/printer"
	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/render"
)

var testClock = time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)

// doc renders fn's output as unstyled markdown, which is deterministic and
// keeps assertions about content free of layout.
func doc(fn func(*printer.Doc)) string {
	var buf bytes.Buffer
	p := printer.New(&buf, printer.FormatMarkdown, false)
	d := p.Doc()
	fn(d)
	_ = d.Render()
	return buf.String()
}

func testInvestigation() *v1alpha1.Investigation {
	return &v1alpha1.Investigation{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "fnd-1-inv-1",
			CreationTimestamp: metav1.NewTime(testClock.Add(-time.Hour)),
		},
		Spec: v1alpha1.InvestigationSpec{
			FindingRef: v1alpha1.ObjectReference{Name: "fnd-1"},
			Attempt:    1,
		},
		Status: v1alpha1.InvestigationStatus{
			Phase:          v1alpha1.RunComplete,
			Recommendation: v1alpha1.RecommendationRemediate,
			Confidence:     "0.92",
			Exploitability: &v1alpha1.Analysis{Rating: v1alpha1.RatingHigh, Summary: "reachable from the CLI entrypoint"},
			Likelihood:     &v1alpha1.Analysis{Rating: v1alpha1.RatingMedium},
			Impact:         &v1alpha1.Analysis{Rating: v1alpha1.RatingCritical},
			Report:         "---\nverdict: remediate\n---\n\n# Analysis\n\nThe input is unsanitised.",
			Stage: &v1alpha1.StageResult{
				Outcome: "ok",
				Model:   "anthropic/claude-sonnet-5",
				Usage:   v1alpha1.UsageSummary{InputTokens: 1000, OutputTokens: 200, CostUSD: "0.031"},
			},
		},
	}
}

// TestInvestigationViewsAgree is the reason this package exists: describe and
// review previously each rendered an investigation their own way, and the two
// spellings of "render a rating" had already diverged. Both depths must report
// the same verdict and the same ratings.
func TestInvestigationViewsAgree(t *testing.T) {
	inv := testInvestigation()
	detail := doc(func(d *printer.Doc) { render.InvestigationDetail(d, inv, testClock) })
	review := doc(func(d *printer.Doc) { render.InvestigationReview(d, inv, false) })

	for _, shared := range []string{
		"remediate",
		"0.92",
		"high — reachable from the CLI entrypoint",
		"medium",
		"critical",
	} {
		if !strings.Contains(detail, shared) {
			t.Errorf("detail view missing %q:\n%s", shared, detail)
		}
		if !strings.Contains(review, shared) {
			t.Errorf("review view missing %q:\n%s", shared, review)
		}
	}
}

// TestReviewStripsFrontmatter: the frontmatter is a contract between stages,
// and pasting it into an issue leaks machine plumbing into a human thread.
func TestReviewStripsFrontmatter(t *testing.T) {
	inv := testInvestigation()

	stripped := doc(func(d *printer.Doc) { render.InvestigationReview(d, inv, false) })
	if strings.Contains(stripped, "verdict: remediate") {
		t.Errorf("frontmatter survived into the report body:\n%s", stripped)
	}
	if !strings.Contains(stripped, "The input is unsanitised.") {
		t.Errorf("report body lost:\n%s", stripped)
	}

	raw := doc(func(d *printer.Doc) { render.InvestigationReview(d, inv, true) })
	if !strings.Contains(raw, "verdict: remediate") {
		t.Errorf("--raw dropped the frontmatter it exists to keep:\n%s", raw)
	}
}

func TestFindingDetail(t *testing.T) {
	f := &v1alpha1.Finding{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "fnd-1",
			CreationTimestamp: metav1.NewTime(testClock.Add(-26 * time.Hour)),
		},
		Spec: v1alpha1.FindingSpec{
			Advisories: []string{"GHSA-xxxx", "CVE-2026-0001"},
			Title:      "Command injection",
			Severity:   v1alpha1.LevelHigh,
			Suspend:    true,
			Alerts: []v1alpha1.Alert{{
				ID:        "42",
				Locations: []v1alpha1.Location{{Path: "cmd/main.go", StartLine: 17}},
			}},
			OverflowAlerts: 3,
		},
		Status: v1alpha1.FindingStatus{
			Phase:  v1alpha1.PhaseQueued,
			Owners: []string{"alice", "bob"},
			Enrichments: []v1alpha1.Enrichment{{
				Enhancer:   "cmdb",
				Attributes: map[string]string{"tier": "1", "env": "prod", "system": "billing"},
			}},
		},
	}

	got := doc(func(d *printer.Doc) { render.FindingDetail(d, f, testClock, "1000 tokens, $0.03") })

	for _, want := range []string{
		"GHSA-xxxx, CVE-2026-0001",
		"1d2h",                           // age
		"cmd/main.go:17",                 // alert location
		"3 more alerts",                  // overflow
		"alice, bob",                     // owners
		"env=prod system=billing tier=1", // attributes, sorted
		"1000 tokens, $0.03",             // caller-supplied spend
		"resume",                         // a suspended finding offers resume
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

// TestEmptySectionsVanish: a detail view padded with empty headings buries the
// fields that say something.
func TestEmptySectionsVanish(t *testing.T) {
	bare := &v1alpha1.Finding{
		ObjectMeta: metav1.ObjectMeta{Name: "fnd-1"},
		Spec:       v1alpha1.FindingSpec{Advisories: []string{"CVE-2026-0001"}},
		Status:     v1alpha1.FindingStatus{Phase: v1alpha1.PhaseOpened},
	}
	got := doc(func(d *printer.Doc) { render.FindingDetail(d, bare, testClock, "") })
	for _, absent := range []string{"Tracking", "Alerts", "Ownership", "Spend"} {
		if strings.Contains(got, "## "+absent) {
			t.Errorf("empty section %q rendered:\n%s", absent, got)
		}
	}
}

func TestRating(t *testing.T) {
	cases := []struct {
		name string
		in   *v1alpha1.Analysis
		want string
	}{
		{name: "absent", in: nil, want: ""},
		{name: "bare", in: &v1alpha1.Analysis{Rating: v1alpha1.RatingLow}, want: "low"},
		{
			name: "with summary",
			in:   &v1alpha1.Analysis{Rating: v1alpha1.RatingHigh, Summary: "reachable"},
			want: "high — reachable",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := render.Rating(tc.in); got != tc.want {
				t.Errorf("Rating = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOutcomeAndUsage(t *testing.T) {
	if got := render.Outcome(nil); got != "" {
		t.Errorf("Outcome(nil) = %q, want empty", got)
	}
	st := &v1alpha1.StageResult{Outcome: "budget_exceeded", Detail: "token ceiling hit"}
	if got, want := render.Outcome(st), "budget_exceeded — token ceiling hit"; got != want {
		t.Errorf("Outcome = %q, want %q", got, want)
	}
	if got := render.Usage(v1alpha1.UsageSummary{}); got != "" {
		t.Errorf("Usage(zero) = %q, want empty", got)
	}
	u := v1alpha1.UsageSummary{InputTokens: 10, OutputTokens: 2, CacheReadTokens: 5, CostUSD: "0.01"}
	if got := render.Usage(u); !strings.Contains(got, "cache read") || !strings.Contains(got, "$0.01") {
		t.Errorf("Usage = %q, want cache and cost detail", got)
	}
}

func TestAge(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "30s"},
		{5 * time.Minute, "5m"},
		{90 * time.Minute, "1h30m"},
		{50 * time.Hour, "2d2h"},
	}
	for _, tc := range cases {
		if got := render.Age(testClock.Add(-tc.d), testClock); got != tc.want {
			t.Errorf("Age(-%s) = %q, want %q", tc.d, got, tc.want)
		}
	}
	if got := render.Age(time.Time{}, testClock); got != "" {
		t.Errorf("Age(zero) = %q, want empty", got)
	}
}

func TestFindingURL(t *testing.T) {
	if got := render.FindingURL(&v1alpha1.Finding{}); got != "" {
		t.Errorf("FindingURL(untracked) = %q, want empty", got)
	}
	f := &v1alpha1.Finding{Status: v1alpha1.FindingStatus{
		Tracking: &v1alpha1.TrackingStatus{URL: "https://example.test/issues/1"},
	}}
	if got := render.FindingURL(f); got != "https://example.test/issues/1" {
		t.Errorf("FindingURL = %q", got)
	}
}
