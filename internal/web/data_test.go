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
	// Contract field names (ui/src/types.ts) on a fully populated finding.
	for _, key := range []string{
		"name", "createdAt", "integration", "source", "repository", "advisories", "ruleID",
		"title", "description", "severity", "alerts", "overflowAlerts", "related", "suspend",
		"approval", "phase", "phaseTimes", "firstObservedAt", "accumulateUntil", "tracking",
		"owners", "enrichments", "priority", "investigation", "remediation", "pullRequest",
		"attempts", "activeRun", "lastFailureReason", "completedAt", "userActions",
	} {
		if _, ok := f[key]; !ok {
			t.Errorf("finding JSON is missing %q", key)
		}
	}
	if f["integration"] != "gh" {
		t.Errorf("integration = %v, want gh (flattened integrationRef.name)", f["integration"])
	}
	if f["createdAt"] != "2026-07-21T10:00:00Z" {
		t.Errorf("createdAt = %v", f["createdAt"])
	}
	// The tracking wire type drops the server-only integration field.
	if tracking := f["tracking"].(map[string]any); tracking["integration"] != nil {
		t.Errorf("tracking leaked integration: %v", tracking)
	}

	// Related edges are named from this finding's perspective.
	rel := ds.Findings[0].Related
	if len(rel) != 2 || rel[0].Name != "gh-cs-orders-0" || rel[1].Name != "gh-cs-billing-9" {
		t.Errorf("related = %+v", rel)
	}
}

func TestBuildDatasetAttachesReports(t *testing.T) {
	inv := &v1alpha1.Investigation{
		ObjectMeta: metav1.ObjectMeta{Name: "gh-cs-orders-1-inv-1", Namespace: "patchy"},
		Status:     v1alpha1.InvestigationStatus{Report: "## Analysis\n\ninjectable"},
	}
	rem := &v1alpha1.Remediation{
		ObjectMeta: metav1.ObjectMeta{Name: "gh-cs-orders-1-rem-1", Namespace: "patchy"},
		Status:     v1alpha1.RemediationStatus{Report: "## Fix\n\nparameterised"},
	}
	s := testServer(t, fullFinding(), inv, rem)
	ds, err := s.buildDataset(t.Context(), true, nil, nil)
	if err != nil {
		t.Fatalf("buildDataset: %v", err)
	}
	f := ds.Findings[0]
	if f.Investigation == nil || f.Investigation.Report != "## Analysis\n\ninjectable" {
		t.Errorf("investigation report = %+v", f.Investigation)
	}
	if f.Remediation == nil || f.Remediation.Report != "## Fix\n\nparameterised" {
		t.Errorf("remediation report = %+v", f.Remediation)
	}

	// An expired/absent child leaves the report empty rather than erroring.
	s = testServer(t, fullFinding())
	if ds, err = s.buildDataset(t.Context(), true, nil, nil); err != nil {
		t.Fatalf("buildDataset without children: %v", err)
	}
	if got := ds.Findings[0].Investigation.Report; got != "" {
		t.Errorf("report without child = %q, want empty", got)
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
		"suspend", "repository", "approval", "phase", "investigation", "remediation",
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
