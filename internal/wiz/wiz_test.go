// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package wiz

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/bitwise-media-group/patchy/pkg/source"
)

// issueJSON is a Wiz Issues delivery shaped exactly as the automation-rule
// body template in docs/integrations/wiz.md renders it. The docs template is
// the payload contract; this fixture must track it.
const issueJSON = `{
	"trigger": {
		"source": "ISSUE",
		"type": "Created",
		"ruleId": "b2f4a891-11a2-44e2-a91b-3c2f8e0f2c0a",
		"ruleName": "patchy-issues"
	},
	"issue": {
		"id": "f2f5d3b8-a663-4c1b-b7f3-8f7f0c8a0001",
		"status": "OPEN",
		"severity": "HIGH",
		"created": "2026-07-26T09:00:00Z",
		"projects": "acme-prod",
		"description": "The bucket allows public read access.",
		"resolutionRecommendation": "Remove allUsers from the bucket IAM policy.",
		"url": "https://app.wiz.io/issues#~(issue~'f2f5d3b8')",
		"control": {
			"id": "wc-id-1234",
			"name": "Bucket accessible to the public",
			"description": "Buckets should not be publicly readable.",
			"severity": "HIGH"
		}
	},
	"entitySnapshot": {
		"id": "3c9c3a6d-entity",
		"type": "BUCKET",
		"nativeType": "storage#bucket",
		"name": "acme-artifacts",
		"cloudPlatform": "GCP",
		"cloudProviderURL": "https://console.cloud.google.com/storage/browser/acme-artifacts",
		"providerId": "https://www.googleapis.com/storage/v1/b/acme-artifacts",
		"region": "europe-west2",
		"subscriptionExternalId": "acme-prod",
		"subscriptionName": "Acme Production",
		"tags": {"team": "storage", "env": "prod"}
	}
}`

// threatJSON is a Wiz Defend delivery per the documented template.
const threatJSON = `{
	"trigger": {
		"source": "THREAT_DETECTION",
		"type": "Created",
		"ruleId": "a1a1a1a1-auto",
		"ruleName": "patchy-defend"
	},
	"threat": {
		"id": "t-0001",
		"name": "Suspicious service account key usage",
		"description": "A service account key was used from an unusual location.",
		"severity": "CRITICAL",
		"status": "OPEN",
		"createdAt": "2026-07-26T10:00:00Z",
		"ruleId": "dr-5678",
		"ruleName": "Unusual key usage",
		"cloudPlatform": "GCP",
		"cloudAccounts": ["acme-prod"],
		"mitreTactics": ["TA0001"],
		"mitreTechniques": ["T1078"],
		"detectionIds": ["d-1", "d-2"],
		"url": "https://app.wiz.io/threats#~(threat~'t-0001')",
		"actors": [{"id": "sa-1", "name": "ci-deployer@acme-prod.iam", "type": "SERVICE_ACCOUNT"}],
		"resources": [
			{
				"id": "r-1",
				"name": "build-vm",
				"type": "VIRTUAL_MACHINE",
				"nativeType": "compute#instance",
				"providerId": "https://www.googleapis.com/compute/v1/projects/acme-prod/zones/europe-west2-a/instances/build-vm",
				"region": "europe-west2-a",
				"cloudPlatform": "GCP",
				"subscriptionExternalId": "acme-prod"
			},
			{
				"id": "r-2",
				"name": "edge-fn",
				"type": "SERVERLESS",
				"nativeType": "AWS::Lambda::Function",
				"providerId": "arn:aws:lambda:eu-west-1:123456789012:function:edge-fn",
				"region": "eu-west-1",
				"cloudPlatform": "AWS",
				"subscriptionExternalId": "123456789012"
			}
		]
	}
}`

// mutate re-renders a fixture with one field on the named object replaced.
func mutate(t *testing.T, fixture, object, field string, value any) []byte {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal([]byte(fixture), &body); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	body[object].(map[string]any)[field] = value
	out, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return out
}

func TestDetect(t *testing.T) {
	for _, tt := range []struct {
		name    string
		body    string
		want    string
		wantErr bool
	}{
		{"issue delivery", issueJSON, EventIssue, false},
		{"threat delivery", threatJSON, EventThreat, false},
		{"test delivery is a ping", `{"text": "test message"}`, "ping", false},
		{"empty object is a ping", `{}`, "ping", false},
		{"undecodable body errors", `{"issue":`, "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Detect([]byte(tt.body))
			if (err != nil) != tt.wantErr {
				t.Fatalf("Detect() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("Detect() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDeliveryID(t *testing.T) {
	a, b := DeliveryID([]byte(issueJSON)), DeliveryID([]byte(threatJSON))
	if a == b {
		t.Error("DeliveryID() gave the same key for distinct bodies")
	}
	if a != DeliveryID([]byte(issueJSON)) {
		t.Error("DeliveryID() is not deterministic")
	}
	if len(a) != 16 {
		t.Errorf("DeliveryID() length = %d, want 16", len(a))
	}
}

func TestIssueFindings(t *testing.T) {
	h := NewIssues(Options{})

	got, err := h.Findings(context.Background(), EventIssue, []byte(issueJSON))
	if err != nil {
		t.Fatalf("Findings() = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("Findings() returned %d findings, want 1", len(got))
	}
	f := got[0]

	// The description is a rendered document; assert its content separately
	// so this comparison stays about the mapping.
	description := f.Description
	f.Description = ""

	want := source.Finding{
		Source:  IssuesID,
		AlertID: "f2f5d3b8-a663-4c1b-b7f3-8f7f0c8a0001",
		CloudResource: &source.CloudResource{
			Provider:    "google",
			Name:        "//storage.googleapis.com/acme-artifacts",
			Type:        "storage#bucket",
			Project:     "acme-prod",
			Location:    "europe-west2",
			DisplayName: "acme-artifacts",
		},
		Advisories: []string{"wiz-control:wc-id-1234"},
		RuleID:     "wc-id-1234",
		Title:      "Bucket accessible to the public",
		Severity:   "high",
		HTMLURL:    "https://app.wiz.io/issues#~(issue~'f2f5d3b8')",
	}
	if !reflect.DeepEqual(f, want) {
		t.Errorf("Findings() mapping mismatch:\n got %+v\nwant %+v", f, want)
	}
	for _, fragment := range []string{
		"public read access",
		"//storage.googleapis.com/acme-artifacts",
		"Remove allUsers",
		"`team` | storage",
	} {
		if !strings.Contains(description, fragment) {
			t.Errorf("description missing %q:\n%s", fragment, description)
		}
	}
}

func TestIssueFindingsSkips(t *testing.T) {
	for _, tt := range []struct {
		name string
		body []byte
		opts Options
	}{
		{"wrong event type", []byte(issueJSON), Options{}},
		{"resolved trigger", mutate(t, issueJSON, "trigger", "type", "Resolved"), Options{}},
		{"rejected status", mutate(t, issueJSON, "issue", "status", "REJECTED"), Options{}},
		{"below the severity floor", mutate(t, issueJSON, "issue", "severity", "LOW"), Options{MinSeverity: "high"}},
		{
			"informational under default floor",
			mutate(t, issueJSON, "issue", "severity", "INFORMATIONAL"),
			Options{MinSeverity: "low"},
		},
		{"unsupported platform", mutate(t, issueJSON, "entitySnapshot", "cloudPlatform", "Kubernetes"), Options{}},
		{"no entity snapshot", mutate(t, issueJSON, "entitySnapshot", "providerId", ""), Options{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			event := EventIssue
			if tt.name == "wrong event type" {
				event = EventThreat
			}
			got, err := NewIssues(tt.opts).Findings(context.Background(), event, tt.body)
			if err != nil {
				t.Fatalf("Findings() = %v, want nil", err)
			}
			if got != nil {
				t.Errorf("Findings() = %d findings, want none", len(got))
			}
		})
	}
}

func TestIssueFindingsMissingID(t *testing.T) {
	if _, err := NewIssues(Options{}).Findings(
		context.Background(), EventIssue, mutate(t, issueJSON, "issue", "id", "")); err == nil {
		t.Error("Findings(issue without id) = nil error, want one")
	}
}

func TestThreatFindings(t *testing.T) {
	h := NewDefend(Options{})

	got, err := h.Findings(context.Background(), EventThreat, []byte(threatJSON))
	if err != nil {
		t.Fatalf("Findings() = %v, want nil", err)
	}
	if len(got) != 2 {
		t.Fatalf("Findings() returned %d findings, want one per resource (2)", len(got))
	}

	gcp, aws := got[0], got[1]
	if gcp.CloudResource == nil || aws.CloudResource == nil {
		t.Fatal("Findings() returned findings without cloud resources")
	}
	wantName := "//compute.googleapis.com/projects/acme-prod/zones/europe-west2-a/instances/build-vm"
	if gcp.CloudResource.Name != wantName {
		t.Errorf("gcp resource name = %q, want normalized %q", gcp.CloudResource.Name, wantName)
	}
	if gcp.CloudResource.Provider != "google" || aws.CloudResource.Provider != "aws" {
		t.Errorf("providers = %q/%q, want google/aws", gcp.CloudResource.Provider, aws.CloudResource.Provider)
	}
	if want := "arn:aws:lambda:eu-west-1:123456789012:function:edge-fn"; aws.CloudResource.Name != want {
		t.Errorf("aws resource name = %q, want untouched %q", aws.CloudResource.Name, want)
	}
	wantAdvisories := []string{"wiz-rule:dr-5678", "T1078"}
	if !reflect.DeepEqual(gcp.Advisories, wantAdvisories) {
		t.Errorf("advisories = %v, want %v", gcp.Advisories, wantAdvisories)
	}
	if gcp.AlertID != "t-0001" || gcp.Source != DefendID || gcp.Severity != "critical" {
		t.Errorf("identity mismatch: %+v", gcp)
	}
	if gcp.Title != "Suspicious service account key usage" {
		t.Errorf("title = %q", gcp.Title)
	}
	for _, fragment := range []string{"unusual location", "ci-deployer@acme-prod.iam (service_account)", "TA0001"} {
		if !strings.Contains(gcp.Description, fragment) {
			t.Errorf("description missing %q", fragment)
		}
	}
}

func TestThreatFindingsAccountFallback(t *testing.T) {
	body := mutate(t, threatJSON, "threat", "resources", []any{})
	got, err := NewDefend(Options{}).Findings(context.Background(), EventThreat, body)
	if err != nil {
		t.Fatalf("Findings() = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("Findings() returned %d findings, want 1 per cloud account", len(got))
	}
	cr := got[0].CloudResource
	if cr == nil || cr.Name != "wiz-account:acme-prod" || cr.Provider != "google" {
		t.Errorf("fallback resource = %+v, want wiz-account:acme-prod on google", cr)
	}
}

func TestThreatFindingsNoSubject(t *testing.T) {
	body := mutate(t, threatJSON, "threat", "resources", []any{})
	body = mutate(t, string(body), "threat", "cloudAccounts", []any{})
	if _, err := NewDefend(Options{}).Findings(context.Background(), EventThreat, body); err == nil {
		t.Error("Findings(threat without subject) = nil error, want one")
	}
}

func TestMeetsMinSeverity(t *testing.T) {
	for _, tt := range []struct {
		severity, floor string
		want            bool
	}{
		{"CRITICAL", "low", true},
		{"LOW", "low", true},
		{"INFORMATIONAL", "low", false},
		{"MEDIUM", "high", false},
		{"HIGH", "", true},
		{"INFORMATIONAL", "", true},
		{"HIGH", "bogus", true}, // unrecognized floor must not drop findings
	} {
		if got := meetsMinSeverity(tt.severity, tt.floor); got != tt.want {
			t.Errorf("meetsMinSeverity(%q, %q) = %v, want %v", tt.severity, tt.floor, got, tt.want)
		}
	}
}
