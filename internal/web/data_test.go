// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package web

import (
	"encoding/json"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/kube"
)

var testClock = time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)

// fullFinding exercises every projected field.
func fullFinding() *v1alpha1.Finding {
	at := metav1.NewTime(testClock.Add(-2 * time.Hour))
	done := metav1.NewTime(testClock.Add(-time.Hour))
	return &v1alpha1.Finding{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "gh-cs-orders-1",
			Namespace:         "patchy",
			CreationTimestamp: at,
		},
		Spec: v1alpha1.FindingSpec{
			IntegrationRef: v1alpha1.LocalObjectReference{Name: "gh"},
			Source:         "github-code-scanning",
			Repository: &v1alpha1.FindingRepository{
				Type: v1alpha1.RepositoryTypeGitHub,
				URL:  "https://github.com/acme/orders",
				Name: "acme/orders", DefaultBranch: "main",
			},
			// A finding normally carries one or the other; this fixture sets
			// both so the projection's whole field set is exercised.
			CloudResource: &v1alpha1.FindingCloudResource{
				Provider:    v1alpha1.CloudProviderGoogle,
				Name:        "//storage.googleapis.com/projects/acme-prod/buckets/artifacts",
				Type:        "google.cloud.storage.Bucket",
				Project:     "projects/acme-prod",
				Location:    "europe-west2",
				DisplayName: "artifacts",
			},
			Advisories:  []string{"CVE-2026-0001", "CWE-89"},
			RuleID:      "go/sql-injection",
			Title:       "SQL injection",
			Description: "user input reaches a query",
			Severity:    v1alpha1.LevelHigh,
			Alerts: []v1alpha1.Alert{{
				ID: "42", URL: "https://github.com/acme/orders/security/code-scanning/42",
				Locations: []v1alpha1.Location{{Path: "db.go", StartLine: 10, EndLine: 12, Snippet: "q := ..."}},
			}},
			OverflowAlerts: 3,
			Related: []v1alpha1.RelatedFinding{
				{From: "gh-cs-orders-1", To: "gh-cs-orders-0", Relationship: v1alpha1.RelationshipSuccessorOf},
				{From: "gh-cs-billing-9", To: "gh-cs-orders-1", Relationship: v1alpha1.RelationshipRelatedTo},
			},
			Suspend:  true,
			Approval: &v1alpha1.Approval{By: "dev", At: at, Note: "ship it"},
		},
		Status: v1alpha1.FindingStatus{
			Phase:           v1alpha1.PhaseHandedOff,
			PhaseTimes:      []v1alpha1.PhaseTime{{Phase: v1alpha1.PhaseOpened, At: at}},
			FirstObservedAt: &at,
			AccumulateUntil: &done,
			Tracking: &v1alpha1.TrackingStatus{
				Integration: "gh", IssueNumber: 7,
				URL: "https://github.com/acme/orders/issues/7", State: "open",
			},
			Owners: []string{"dev"},
			Enrichments: []v1alpha1.Enrichment{
				{Enhancer: "cmdb", Owners: []string{"team-a"}, Markdown: "owner: team-a", AppliedAt: at},
			},
			Priority: v1alpha1.LevelCritical,
			Investigation: &v1alpha1.InvestigationSummary{
				Name: "gh-cs-orders-1-inv-1", Attempt: 1, Outcome: "ok",
				Recommendation: v1alpha1.RecommendationRemediate, Confidence: "0.92",
				Exploitability: v1alpha1.RatingHigh, Likelihood: v1alpha1.RatingMedium,
				Impact: v1alpha1.RatingCritical, AwaitApproval: true, CompletedAt: &done,
			},
			Remediation: &v1alpha1.RemediationSummary{
				Name: "gh-cs-orders-1-rem-1", Attempt: 1, Outcome: "ok",
				Success: true, Branch: "patchy/gh-cs-orders-1", CompletedAt: &done,
			},
			PullRequest: &v1alpha1.PullRequestStatus{
				Number: 11, URL: "https://github.com/acme/orders/pull/11",
				State: "merged", MergedAt: &done,
			},
			Attempts:          v1alpha1.AttemptCounts{Investigation: 1, Remediation: 2},
			ActiveRun:         &v1alpha1.ActiveRun{Kind: v1alpha1.RunKindRemediation, Name: "gh-cs-orders-1-rem-1"},
			LastFailureReason: "previous attempt timed out",
			CompletedAt:       &done,
		},
	}
}

func testRollup(scope v1alpha1.ScopeType, key, name string) *v1alpha1.FindingRollup {
	at := metav1.NewTime(testClock.Add(-24 * time.Hour))
	return &v1alpha1.FindingRollup{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "patchy"},
		Spec:       v1alpha1.FindingRollupSpec{Scope: v1alpha1.RollupScope{Type: scope, Key: key}},
		Status: v1alpha1.FindingRollupStatus{
			SchemaVersion:  1,
			FirstProcessed: &at,
			LastProcessed:  &at,
			Bucket: v1alpha1.RollupBucket{
				Findings:        10,
				Phases:          map[string]int64{"remediated": 7, "failed": 3},
				Recommendations: map[string]int64{"remediate": 8},
				Attempts:        14,
				Stages: map[string]v1alpha1.StageAggregate{
					"investigation": {
						Runs: 12, Succeeded: 11, InputTokens: 1000,
						CostMicroUSD: 12_500_000, ElapsedMilliseconds: 90_000,
					},
				},
			},
			Monthly: map[string]v1alpha1.MonthlyBucket{"2026-07": {Findings: 4, Runs: 6, CostMicroUSD: 5_000_000}},
			Recent:  []string{"i:abc"},
		},
	}
}

func testServer(t *testing.T, objs ...client.Object) *Server {
	t.Helper()
	c := fake.NewClientBuilder().
		WithScheme(kube.Scheme()).
		WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.Finding{}, &v1alpha1.FindingRollup{}).
		WithIndex(&v1alpha1.Investigation{}, RunFindingIndex, RunFindingIndexer).
		WithIndex(&v1alpha1.Remediation{}, RunFindingIndex, RunFindingIndexer).
		Build()
	s := NewServer(c, "patchy", stubAuth{}, stubGranter{}, nil)
	s.now = func() time.Time { return testClock }
	return s
}

// asMap round-trips a value through JSON for field-name assertions.
func asMap(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}

func TestBuildDatasetProjection(t *testing.T) {
	s := testServer(t, fullFinding(), testRollup(v1alpha1.ScopeTotal, "", "total"))
	ds, err := s.buildDataset(t.Context(), true, []string{"approve"}, &User{Name: "dev", LoggedIn: true})
	if err != nil {
		t.Fatalf("buildDataset: %v", err)
	}
	if ds.GeneratedAt != "2026-07-21T12:00:00Z" || ds.Namespace != "patchy" {
		t.Errorf("stamps = %q/%q", ds.GeneratedAt, ds.Namespace)
	}
	if ds.User == nil || ds.User.Name != "dev" || !ds.User.LoggedIn {
		t.Errorf("user = %+v", ds.User)
	}
	if len(ds.Findings) != 1 || len(ds.Rollups) != 1 {
		t.Fatalf("findings/rollups = %d/%d, want 1/1", len(ds.Findings), len(ds.Rollups))
	}

	f := asMap(t, ds.Findings[0])
	// Contract field names (ui/src/types.ts FindingSummary) on a fully
	// populated finding.
	for _, key := range []string{
		"name", "createdAt", "integration", "source", "repository", "cloudResource",
		"advisories", "ruleID", "title", "severity", "suspend", "phase",
		"firstObservedAt", "accumulateUntil", "owners", "priority",
		"investigation", "remediation", "pullRequest",
		"attempts", "activeRun", "lastFailureReason", "completedAt", "userActions",
	} {
		if _, ok := f[key]; !ok {
			t.Errorf("finding summary JSON is missing %q", key)
		}
	}
	// The unbounded detail stays off the list payload — that is the whole
	// point of the split (the detail route carries it).
	for _, key := range []string{
		"description", "alerts", "overflowAlerts", "related", "approval",
		"phaseTimes", "tracking", "enrichments", "totalUsage",
	} {
		if _, ok := f[key]; ok {
			t.Errorf("finding summary JSON leaked detail field %q", key)
		}
	}
	if inv := f["investigation"].(map[string]any); inv["report"] != nil {
		t.Errorf("summary investigation leaked report: %v", inv)
	}
	if f["integration"] != "gh" {
		t.Errorf("integration = %v, want gh (flattened integrationRef.name)", f["integration"])
	}
	if f["createdAt"] != "2026-07-21T10:00:00Z" {
		t.Errorf("createdAt = %v", f["createdAt"])
	}
}

func TestBuildFindingDetailProjection(t *testing.T) {
	s := testServer(t, fullFinding())
	out, err := s.buildFindingDetail(t.Context(), "gh-cs-orders-1", []string{"approve"})
	if err != nil {
		t.Fatalf("buildFindingDetail: %v", err)
	}
	f := asMap(t, out)
	// Contract field names (ui/src/types.ts Finding) on a fully populated
	// finding: the summary fields plus the detail-only ones.
	for _, key := range []string{
		"name", "createdAt", "integration", "source", "repository", "cloudResource",
		"advisories", "ruleID",
		"title", "description", "severity", "alerts", "overflowAlerts", "related", "suspend",
		"approval", "phase", "phaseTimes", "firstObservedAt", "accumulateUntil", "tracking",
		"owners", "enrichments", "priority", "investigation", "remediation", "pullRequest",
		"attempts", "activeRun", "lastFailureReason", "completedAt", "userActions",
	} {
		if _, ok := f[key]; !ok {
			t.Errorf("finding detail JSON is missing %q", key)
		}
	}
	// The tracking wire type drops the server-only integration field.
	if tracking := f["tracking"].(map[string]any); tracking["integration"] != nil {
		t.Errorf("tracking leaked integration: %v", tracking)
	}

	// Related edges are named from this finding's perspective.
	rel := out.Related
	if len(rel) != 2 || rel[0].Name != "gh-cs-orders-0" || rel[1].Name != "gh-cs-billing-9" {
		t.Errorf("related = %+v", rel)
	}
}

func TestBuildFindingDetailAttachesRunDetail(t *testing.T) {
	childMeta := func(name string) metav1.ObjectMeta {
		return metav1.ObjectMeta{
			Name: name, Namespace: "patchy",
			Labels: map[string]string{v1alpha1.LabelFinding: "gh-cs-orders-1"},
		}
	}
	// A failed first attempt plus the summarised second: totals span both.
	invOld := &v1alpha1.Investigation{
		ObjectMeta: childMeta("gh-cs-orders-1-inv-0"),
		Status: v1alpha1.InvestigationStatus{
			Stage: &v1alpha1.StageResult{Outcome: "timeout", Usage: v1alpha1.UsageSummary{
				InputTokens: 100, OutputTokens: 10, CostUSD: "0.25",
			}},
		},
	}
	inv := &v1alpha1.Investigation{
		ObjectMeta: childMeta("gh-cs-orders-1-inv-1"),
		Status: v1alpha1.InvestigationStatus{
			Report: "## Analysis\n\ninjectable",
			Stage: &v1alpha1.StageResult{
				Outcome: "ok", Harness: "claude-code", Model: "claude-sonnet-5",
				Usage: v1alpha1.UsageSummary{
					InputTokens: 1000, OutputTokens: 200,
					CacheReadTokens: 5000, CacheCreationTokens: 300, CostUSD: "1.50",
				},
			},
		},
	}
	rem := &v1alpha1.Remediation{
		ObjectMeta: childMeta("gh-cs-orders-1-rem-1"),
		Status: v1alpha1.RemediationStatus{
			Report: "## Fix\n\nparameterised",
			Stage: &v1alpha1.StageResult{
				Outcome: "ok", Harness: "claude-code", Model: "claude-sonnet-5",
				Usage: v1alpha1.UsageSummary{InputTokens: 400, OutputTokens: 80, CostUSD: "0.75"},
			},
		},
	}
	s := testServer(t, fullFinding(), invOld, inv, rem)
	f, err := s.buildFindingDetail(t.Context(), "gh-cs-orders-1", nil)
	if err != nil {
		t.Fatalf("buildFindingDetail: %v", err)
	}
	if f.Investigation == nil || f.Investigation.Report != "## Analysis\n\ninjectable" {
		t.Errorf("investigation report = %+v", f.Investigation)
	}
	if f.Investigation.Harness != "claude-code" || f.Investigation.Model != "claude-sonnet-5" {
		t.Errorf("investigation harness/model = %q/%q", f.Investigation.Harness, f.Investigation.Model)
	}
	if u := f.Investigation.Usage; u == nil || u.InputTokens != 1000 || u.CostMicroUSD != 1_500_000 {
		t.Errorf("investigation usage = %+v", u)
	}
	if f.Remediation == nil || f.Remediation.Report != "## Fix\n\nparameterised" {
		t.Errorf("remediation report = %+v", f.Remediation)
	}
	if u := f.Remediation.Usage; u == nil || u.CostMicroUSD != 750_000 {
		t.Errorf("remediation usage = %+v", u)
	}
	want := Usage{
		InputTokens: 1500, OutputTokens: 290,
		CacheReadTokens: 5000, CacheCreationTokens: 300, CostMicroUSD: 2_500_000,
	}
	if f.TotalUsage == nil || *f.TotalUsage != want {
		t.Errorf("totalUsage = %+v, want %+v", f.TotalUsage, want)
	}

	// An expired/absent child leaves report and accounting empty rather
	// than erroring.
	s = testServer(t, fullFinding())
	if f, err = s.buildFindingDetail(t.Context(), "gh-cs-orders-1", nil); err != nil {
		t.Fatalf("buildFindingDetail without children: %v", err)
	}
	if f.Investigation.Report != "" || f.Investigation.Usage != nil || f.TotalUsage != nil {
		t.Errorf("run detail without children = %+v / total %+v, want empty", f.Investigation, f.TotalUsage)
	}
}

// A sibling finding's children never bleed in: the child lookup is by
// owning-finding label, not "whatever the namespace holds".
func TestBuildFindingDetailIsolatesSiblings(t *testing.T) {
	inv := &v1alpha1.Investigation{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gh-cs-orders-1-inv-1", Namespace: "patchy",
			Labels: map[string]string{v1alpha1.LabelFinding: "gh-cs-orders-1"},
		},
		Status: v1alpha1.InvestigationStatus{
			Report: "## Analysis\n\ninjectable",
			Stage: &v1alpha1.StageResult{
				Outcome: "ok", Usage: v1alpha1.UsageSummary{InputTokens: 100, CostUSD: "0.25"},
			},
		},
	}
	// The sibling references the same child name in its status; only the
	// label decides ownership.
	other := fullFinding()
	other.Name = "gh-cs-billing-9"
	s := testServer(t, fullFinding(), other, inv)
	f, err := s.buildFindingDetail(t.Context(), "gh-cs-billing-9", nil)
	if err != nil {
		t.Fatalf("buildFindingDetail sibling: %v", err)
	}
	if f.Investigation.Report != "" || f.TotalUsage != nil {
		t.Errorf("sibling absorbed another finding's children: %+v / %+v", f.Investigation, f.TotalUsage)
	}
}

func TestBuildDatasetRollupProjection(t *testing.T) {
	s := testServer(t, testRollup(v1alpha1.ScopeTotal, "", "total"))
	ds, err := s.buildDataset(t.Context(), false, nil, nil)
	if err != nil {
		t.Fatalf("buildDataset: %v", err)
	}
	if len(ds.Rollups) != 1 {
		t.Fatalf("rollups = %d, want 1", len(ds.Rollups))
	}
	r := asMap(t, ds.Rollups[0])
	if _, ok := r["scope"]; !ok {
		t.Error("rollup JSON is missing scope")
	}
	for _, key := range []string{"schemaVersion", "recent"} {
		if _, ok := r[key]; ok {
			t.Errorf("rollup leaked server-only field %q", key)
		}
	}
	if agg := ds.Rollups[0].Bucket.Stages["investigation"]; agg.CostMicroUSD != 12_500_000 {
		t.Errorf("stage cost = %d", agg.CostMicroUSD)
	}
	if ds.Rollups[0].Monthly["2026-07"].Runs != 6 {
		t.Errorf("monthly = %+v", ds.Rollups[0].Monthly)
	}
}

func TestBuildDatasetMinimalFinding(t *testing.T) {
	fnd := &v1alpha1.Finding{
		ObjectMeta: metav1.ObjectMeta{Name: "bare", Namespace: "patchy"},
		Spec: v1alpha1.FindingSpec{
			IntegrationRef: v1alpha1.LocalObjectReference{Name: "gh"},
			Source:         "github-code-scanning",
			Advisories:     []string{"CVE-2026-0002"},
		},
	}
	s := testServer(t, fnd)
	ds, err := s.buildDataset(t.Context(), true, nil, nil)
	if err != nil {
		t.Fatalf("buildDataset: %v", err)
	}
	f := asMap(t, ds.Findings[0])
	// Optional zero values are omitted, not rendered as false/""/null noise.
	for _, key := range []string{
		"suspend", "repository", "phase", "investigation", "remediation",
		"pullRequest", "attempts", "activeRun", "completedAt", "userActions", "createdAt",
	} {
		if _, ok := f[key]; ok {
			t.Errorf("minimal finding rendered %q", key)
		}
	}
	if _, ok := f["advisories"]; !ok {
		t.Error("advisories missing (required field)")
	}
}

func TestBuildDatasetOrderingAndRollupsOnly(t *testing.T) {
	older := fullFinding()
	newer := fullFinding()
	newer.Name = "gh-cs-orders-2"
	at := metav1.NewTime(testClock.Add(-time.Minute))
	newer.Status.FirstObservedAt = &at
	s := testServer(t, older, newer,
		testRollup(v1alpha1.ScopeRepository, "acme/orders", "repo-x"),
		testRollup(v1alpha1.ScopeTotal, "", "total"))

	ds, err := s.buildDataset(t.Context(), true, nil, nil)
	if err != nil {
		t.Fatalf("buildDataset: %v", err)
	}
	if ds.Findings[0].Name != "gh-cs-orders-2" {
		t.Errorf("order = [%s, %s], want newest first", ds.Findings[0].Name, ds.Findings[1].Name)
	}
	if ds.Rollups[0].Scope.Type != "repository" || ds.Rollups[1].Scope.Type != "total" {
		t.Errorf("rollup order = %+v", ds.Rollups)
	}

	pub, err := s.buildDataset(t.Context(), false, nil, nil)
	if err != nil {
		t.Fatalf("buildDataset(rollups): %v", err)
	}
	m := asMap(t, pub)
	if got := m["findings"].([]any); len(got) != 0 {
		t.Errorf("rollups-only dataset carried %d findings", len(got))
	}
	if _, ok := m["user"]; ok {
		t.Error("rollups-only dataset carried user")
	}
	if len(pub.Rollups) != 2 {
		t.Errorf("rollups-only dataset rollups = %d", len(pub.Rollups))
	}
}
