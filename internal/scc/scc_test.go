// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package scc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/bitwise-media-group/patchy/pkg/source"
)

// notificationJSON is a Security Command Center NotificationMessage, trimmed
// to the fields patchy reads but otherwise shaped as SCC emits them.
const notificationJSON = `{
	"notificationConfigName": "organizations/1234567890/locations/global/notificationConfigs/patchy",
	"finding": {
		"name": "organizations/1234567890/sources/555/findings/abc123",
		"parent": "organizations/1234567890/sources/555",
		"resourceName": "//storage.googleapis.com/projects/acme-prod/buckets/acme-artifacts",
		"state": "ACTIVE",
		"category": "PUBLIC_BUCKET_ACL",
		"externalUri": "https://console.cloud.google.com/security/command-center/findings?f=abc123",
		"severity": "HIGH",
		"mute": "UNMUTED",
		"findingClass": "MISCONFIGURATION",
		"description": "The bucket is publicly readable.",
		"nextSteps": "Remove allUsers from the bucket IAM policy.",
		"moduleName": "PUBLIC_BUCKET_ACL",
		"eventTime": "2026-07-26T09:00:00Z",
		"createTime": "2026-07-26T09:00:01Z",
		"compliances": [{"standard": "cis", "version": "1.3", "ids": ["5.1"]}],
		"sourceProperties": {"ExceptionInstructions": "Add the mark", "Recommendation": "Restrict access"},
		"securityMarks": {
			"name": "organizations/1234567890/sources/555/findings/abc123/securityMarks",
			"marks": {"scm-repository-org": "acme", "scm-repository-name": "infra-prod"}
		}
	},
	"resource": {
		"name": "//storage.googleapis.com/projects/acme-prod/buckets/acme-artifacts",
		"displayName": "acme-artifacts",
		"type": "google.cloud.storage.Bucket",
		"project": "projects/acme-prod",
		"projectDisplayName": "acme-prod",
		"location": "europe-west2",
		"service": "storage.googleapis.com",
		"cloudProvider": "GOOGLE_CLOUD_PLATFORM"
	}
}`

// push wraps a NotificationMessage in the Pub/Sub envelope, as a push
// subscription delivers it.
func push(t *testing.T, notificationJSON string) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"subscription": "projects/acme-prod/subscriptions/patchy-scc-push",
		"message": map[string]any{
			"data":            base64.StdEncoding.EncodeToString([]byte(notificationJSON)),
			"messageId":       "2070443601311540",
			"publishTime":     "2026-07-26T09:00:02Z",
			"deliveryAttempt": 1,
		},
	})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return body
}

// mutate re-renders the fixture with one finding field replaced.
func mutate(t *testing.T, field string, value any) string {
	t.Helper()
	var n map[string]any
	if err := json.Unmarshal([]byte(notificationJSON), &n); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	n["finding"].(map[string]any)[field] = value
	out, err := json.Marshal(n)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return string(out)
}

func TestFindings(t *testing.T) {
	h := New(Options{Organization: "1234567890"})

	got, err := h.Findings(context.Background(), EventType, push(t, notificationJSON))
	if err != nil {
		t.Fatalf("Findings() = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("Findings() returned %d findings, want 1", len(got))
	}
	f := got[0]

	// The description is a rendered document; assert its content separately so
	// this comparison stays about the mapping.
	description := f.Description
	f.Description = ""

	want := source.Finding{
		Source:  ID,
		AlertID: "organizations/1234567890/sources/555/findings/abc123",
		CloudResource: &source.CloudResource{
			Provider:    "google",
			Name:        "//storage.googleapis.com/projects/acme-prod/buckets/acme-artifacts",
			Type:        "google.cloud.storage.Bucket",
			Project:     "projects/acme-prod",
			Location:    "europe-west2",
			DisplayName: "acme-artifacts",
		},
		Advisories: []string{"category:PUBLIC_BUCKET_ACL"},
		RuleID:     "PUBLIC_BUCKET_ACL",
		Title:      "Public bucket acl",
		Severity:   "high",
		HTMLURL:    "https://console.cloud.google.com/security/command-center/findings?f=abc123",
	}
	if !reflect.DeepEqual(f, want) {
		t.Errorf("Findings()[0] =\n%+v\nwant\n%+v", f, want)
	}

	// A cloud finding names no repository: resolving one is a later, separate
	// job for the enhancer chain.
	if got[0].Repo != (source.Repo{}) {
		t.Errorf("Repo = %+v, want zero", got[0].Repo)
	}

	for _, want := range []string{
		"The bucket is publicly readable.",
		"Remove allUsers from the bucket IAM policy.",
		"`//storage.googleapis.com/projects/acme-prod/buckets/acme-artifacts`",
		"scm-repository-org",
	} {
		if !strings.Contains(description, want) {
			t.Errorf("description missing %q:\n%s", want, description)
		}
	}
}

func TestFindingsSkips(t *testing.T) {
	for _, tt := range []struct {
		name    string
		event   string
		payload func(*testing.T) []byte
	}{
		{
			name:    "another source's event",
			event:   "code_scanning_alert",
			payload: func(t *testing.T) []byte { return push(t, notificationJSON) },
		},
		{
			// SCC re-notifies when a finding is resolved; patchy triages open
			// problems, and its own TTL handles the rest.
			name:    "an inactive finding",
			event:   EventType,
			payload: func(t *testing.T) []byte { return push(t, mutate(t, "state", "INACTIVE")) },
		},
		{
			// A human already decided this one is noise.
			name:    "a muted finding",
			event:   EventType,
			payload: func(t *testing.T) []byte { return push(t, mutate(t, "mute", "MUTED")) },
		},
		{
			// SCC_ERROR means a detector could not run: an operational problem
			// for whoever owns the SCC config, not a finding to remediate.
			name:    "a detector error",
			event:   EventType,
			payload: func(t *testing.T) []byte { return push(t, mutate(t, "findingClass", "SCC_ERROR")) },
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := New(Options{}).Findings(context.Background(), tt.event, tt.payload(t))
			if err != nil {
				t.Fatalf("Findings() = %v, want nil", err)
			}
			if got != nil {
				t.Errorf("Findings() = %+v, want nil", got)
			}
		})
	}
}

func TestFindingsMinSeverity(t *testing.T) {
	for _, tt := range []struct {
		name     string
		floor    string
		severity string
		want     bool
	}{
		{"at the floor", "high", "HIGH", true},
		{"above the floor", "high", "CRITICAL", true},
		{"below the floor", "high", "MEDIUM", false},
		{"unrated is below every floor", "low", "SEVERITY_UNSPECIFIED", false},
		{"no floor admits an unrated finding", "", "SEVERITY_UNSPECIFIED", true},
		// A floor patchy does not recognize must not silently swallow
		// findings; failing open is the safe direction for a security tool.
		{"an unrecognized floor admits everything", "nonsense", "LOW", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := New(Options{MinSeverity: tt.floor})
			got, err := h.Findings(context.Background(), EventType,
				push(t, mutate(t, "severity", tt.severity)))
			if err != nil {
				t.Fatalf("Findings() = %v, want nil", err)
			}
			if admitted := len(got) == 1; admitted != tt.want {
				t.Errorf("admitted = %v, want %v", admitted, tt.want)
			}
		})
	}
}

func TestFindingsErrors(t *testing.T) {
	for _, tt := range []struct {
		name    string
		payload []byte
	}{
		{"not json", []byte("{")},
		{"envelope with no message data", []byte(`{"message":{"messageId":"1"}}`)},
		{"message data that is not base64", []byte(`{"message":{"data":"!!!!"}}`)},
		{
			"message data that is not a notification",
			[]byte(`{"message":{"data":"` +
				base64.StdEncoding.EncodeToString([]byte("nope")) + `"}}`),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(Options{}).Findings(context.Background(), EventType, tt.payload); err == nil {
				t.Error("Findings() = nil error, want a decode failure")
			}
		})
	}

	t.Run("a notification with no finding name", func(t *testing.T) {
		if _, err := New(Options{}).Findings(context.Background(), EventType,
			push(t, mutate(t, "name", ""))); err == nil {
			t.Error("Findings() = nil error, want a validation failure")
		}
	})
}

func TestAdvisories(t *testing.T) {
	for _, tt := range []struct {
		name string
		f    finding
		want []string
	}{
		{
			// The common case: a misconfiguration is not a vulnerability, so
			// the category is what keys accumulation.
			name: "category only",
			f:    finding{Category: "PUBLIC_BUCKET_ACL"},
			want: []string{"category:PUBLIC_BUCKET_ACL"},
		},
		{
			name: "cve outranks category",
			f: finding{
				Category:      "OS_VULNERABILITY",
				Vulnerability: cve("cve-2026-1234"),
			},
			want: []string{"CVE-2026-1234", "category:OS_VULNERABILITY"},
		},
		{
			// The CR requires at least one advisory, so there is always a
			// fallback rather than a rejected finding.
			name: "neither falls back to the finding id",
			f:    finding{Name: "organizations/1/sources/2/findings/abc"},
			want: []string{"scc:abc"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := advisories(&tt.f); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("advisories() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHumanize(t *testing.T) {
	for in, want := range map[string]string{
		"PUBLIC_BUCKET_ACL":       "Public bucket acl",
		"OVER_PRIVILEGED_ACCOUNT": "Over privileged account",
		"MFA_NOT_ENFORCED":        "Mfa not enforced",
		"SINGLE":                  "Single",
	} {
		if got := humanize(in); got != want {
			t.Errorf("humanize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDeliveryID(t *testing.T) {
	if got := DeliveryID(push(t, notificationJSON)); got != "2070443601311540" {
		t.Errorf("DeliveryID() = %q, want the message id", got)
	}
	// Pub/Sub sends both spellings; either alone must work.
	if got := DeliveryID([]byte(`{"message":{"message_id":"42"}}`)); got != "42" {
		t.Errorf("DeliveryID(snake_case) = %q, want 42", got)
	}
	// An undecodable body cannot be deduped, but it is the authenticator's
	// job to reject it, not this function's.
	if got := DeliveryID([]byte("{")); got != "" {
		t.Errorf("DeliveryID(garbage) = %q, want empty", got)
	}
}

// cve builds a vulnerability block carrying just an id.
func cve(id string) *vulnerability {
	v := &vulnerability{}
	v.CVE = &struct {
		ID              string `json:"id"`
		UpstreamFixTime string `json:"upstreamFixAvailable"`
		Cvssv3          *struct {
			BaseScore float64 `json:"baseScore"`
		} `json:"cvssv3"`
	}{ID: id}
	return v
}
