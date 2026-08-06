// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package generic

import (
	"context"
	"strings"
	"testing"
)

const validDelivery = `{
  "version": "v1",
  "event": "findings",
  "findings": [
    {
      "repo": {"owner": "bitwise-media-group", "name": "shop"},
      "alertId": "wh-1001",
      "advisories": ["CVE-2026-1234", "CWE-89"],
      "ruleId": "sql-injection",
      "title": "SQL injection in the order lookup",
      "description": "User input reaches the query builder unsanitized.",
      "severity": "high",
      "htmlUrl": "https://warehouse.internal/findings/1001",
      "locations": [{"path": "internal/orders/lookup.go", "start_line": 42}]
    },
    {
      "cloudResource": {"provider": "google", "name": "//storage.googleapis.com/b"},
      "alertNumber": 7,
      "title": "Public bucket",
      "severity": "low"
    }
  ]
}`

func TestSourceFindings(t *testing.T) {
	s := NewSource("warehouse", Options{})
	if s.ID() != "warehouse" {
		t.Fatalf("ID() = %q, want the integration name", s.ID())
	}

	got, err := s.Findings(context.Background(), EventFindings, []byte(validDelivery))
	if err != nil {
		t.Fatalf("Findings() = %v, want nil", err)
	}
	if len(got) != 2 {
		t.Fatalf("Findings() returned %d findings, want 2", len(got))
	}
	code, cloud := got[0], got[1]
	if code.Source != "warehouse" || cloud.Source != "warehouse" {
		t.Errorf("sources = %q/%q, want the integration name on every finding", code.Source, cloud.Source)
	}
	if code.Repo.String() != "bitwise-media-group/shop" || code.AlertID != "wh-1001" {
		t.Errorf("code finding = %+v, want repo and alert id mapped", code)
	}
	if code.Advisories[0] != "CVE-2026-1234" {
		t.Errorf("advisories = %v, want the delivered ordering kept", code.Advisories)
	}
	if len(code.Locations) != 1 || code.Locations[0].Path != "internal/orders/lookup.go" {
		t.Errorf("locations = %+v, want the delivered location", code.Locations)
	}
	if cloud.CloudResource == nil || cloud.CloudResource.Provider != "google" {
		t.Errorf("cloud finding = %+v, want its cloud resource", cloud)
	}
	if cloud.Advisories[0] != "generic-alert:7" {
		t.Errorf("cloud advisories = %v, want the alert-identity fallback", cloud.Advisories)
	}

	if got, err := s.Findings(context.Background(), "something.else", []byte(validDelivery)); got != nil || err != nil {
		t.Errorf("Findings(foreign event) = (%v, %v), want (nil, nil)", got, err)
	}
}

func TestSourceValidation(t *testing.T) {
	wrap := func(finding string) string {
		return `{"version":"v1","event":"findings","findings":[` + finding + `]}`
	}
	tests := []struct {
		name    string
		payload string
		wantErr string
	}{
		{
			"no findings at all",
			`{"version":"v1","event":"findings"}`,
			"no findings",
		},
		{
			"missing title",
			wrap(`{"repo":{"owner":"o","name":"n"},"alertId":"a","severity":"low"}`),
			"title is required",
		},
		{
			"unknown severity",
			wrap(`{"repo":{"owner":"o","name":"n"},"alertId":"a","title":"t","severity":"URGENT"}`),
			"severity",
		},
		{
			"neither repo nor resource",
			wrap(`{"alertId":"a","title":"t","severity":"low"}`),
			"repo, a cloudResource, or both",
		},
		{
			"half a repo",
			wrap(`{"repo":{"owner":"o"},"alertId":"a","title":"t","severity":"low"}`),
			"owner and name",
		},
		{
			"half a cloud resource",
			wrap(`{"cloudResource":{"provider":"google"},"alertId":"a","title":"t","severity":"low"}`),
			"provider and name",
		},
		{
			"no alert identity",
			wrap(`{"repo":{"owner":"o","name":"n"},"title":"t","severity":"low"}`),
			"alertId or a positive alertNumber",
		},
		{
			"one bad finding fails the batch",
			`{"version":"v1","event":"findings","findings":[
			  {"repo":{"owner":"o","name":"n"},"alertId":"a","title":"t","severity":"low"},
			  {"repo":{"owner":"o","name":"n"},"alertId":"b","severity":"low"}
			]}`,
			"finding 1",
		},
		{
			"undecodable",
			`not json`,
			"decode",
		},
	}
	s := NewSource("warehouse", Options{})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := s.Findings(context.Background(), EventFindings, []byte(tt.payload))
			if err == nil {
				t.Fatalf("Findings() = %v findings and nil error, want an error containing %q", len(got), tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Findings() error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestSourceMinSeverity(t *testing.T) {
	s := NewSource("warehouse", Options{MinSeverity: "medium"})
	got, err := s.Findings(context.Background(), EventFindings, []byte(validDelivery))
	if err != nil {
		t.Fatalf("Findings() = %v, want nil", err)
	}
	if len(got) != 1 || got[0].Severity != "high" {
		t.Errorf("Findings(floor=medium) = %+v, want only the high finding", got)
	}

	// The advisory fallback chain: delivered list > rule > alert identity.
	ruleOnly := `{"version":"v1","event":"findings","findings":[
	  {"repo":{"owner":"o","name":"n"},"alertId":"a","ruleId":"r-1","title":"t","severity":"high"}
	]}`
	got, err = s.Findings(context.Background(), EventFindings, []byte(ruleOnly))
	if err != nil || len(got) != 1 {
		t.Fatalf("Findings(rule only) = (%v, %v), want one finding", got, err)
	}
	if got[0].Advisories[0] != "generic-rule:r-1" {
		t.Errorf("advisories = %v, want the rule fallback", got[0].Advisories)
	}
}
