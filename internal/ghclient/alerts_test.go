// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package ghclient

import (
	"context"
	"net/http"
	"reflect"
	"testing"
)

func TestGetAlert(t *testing.T) {
	tests := []struct {
		name string
		body string
		want *Alert
	}{
		{
			name: "full mapping with security severity",
			body: `{
				"number":4,"state":"open","html_url":"https://gh/o/r/security/code-scanning/4",
				"rule":{
					"id":"go/sql-injection","name":"SQL injection","description":"desc","help":"help text",
					"severity":"warning","security_severity_level":"high",
					"tags":["security","external/cwe/cwe-089"]
				},
				"most_recent_instance":{
					"commit_sha":"abc123",
					"message":{"text":"user input flows here"},
					"location":{"path":"db/query.go","start_line":10,"end_line":12}
				}
			}`,
			want: &Alert{
				Number: 4, RuleID: "go/sql-injection", RuleName: "SQL injection",
				RuleDescription: "desc", RuleHelp: "help text",
				Tags:     []string{"security", "external/cwe/cwe-089"},
				Severity: "high", State: "open",
				HTMLURL: "https://gh/o/r/security/code-scanning/4",
				Path:    "db/query.go", StartLine: 10, EndLine: 12,
				Snippet: "user input flows here", MostRecentSHA: "abc123",
			},
		},
		{
			name: "falls back to rule severity",
			body: `{"number":4,"state":"open","rule":{"id":"go/x","severity":"warning"}}`,
			want: &Alert{Number: 4, RuleID: "go/x", Severity: "warning", State: "open"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux, c := newFakeClient(t)
			mux.HandleFunc("GET /repos/o/r/code-scanning/alerts/4",
				func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, tt.body) })

			got, err := c.GetAlert(context.Background(), testRepo, 4)
			if err != nil {
				t.Fatalf("GetAlert() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetAlert() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestDismissAlert(t *testing.T) {
	mux, c := newFakeClient(t)
	mux.HandleFunc("PATCH /repos/o/r/code-scanning/alerts/4", func(w http.ResponseWriter, r *http.Request) {
		want := map[string]any{
			"state":             "dismissed",
			"dismissed_reason":  "won't fix",
			"dismissed_comment": "accepted risk",
		}
		if body := decodeBody[map[string]any](t, r); !reflect.DeepEqual(body, want) {
			t.Errorf("dismiss request = %v, want %v", body, want)
		}
		writeJSON(t, w, `{"number":4,"state":"dismissed"}`)
	})

	if err := c.DismissAlert(context.Background(), testRepo, 4, "won't fix", "accepted risk"); err != nil {
		t.Errorf("DismissAlert() error = %v, want nil", err)
	}
}

func TestOpenAlert(t *testing.T) {
	mux, c := newFakeClient(t)
	mux.HandleFunc("PATCH /repos/o/r/code-scanning/alerts/4", func(w http.ResponseWriter, r *http.Request) {
		if body := decodeBody[map[string]any](t, r); !reflect.DeepEqual(body, map[string]any{"state": "open"}) {
			t.Errorf("open request = %v, want state open only", body)
		}
		writeJSON(t, w, `{"number":4,"state":"open"}`)
	})

	if err := c.OpenAlert(context.Background(), testRepo, 4); err != nil {
		t.Errorf("OpenAlert() error = %v, want nil", err)
	}
}
