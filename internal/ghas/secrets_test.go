// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package ghas

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/bitwise-media-group/patchy/internal/ghclient"
	"github.com/bitwise-media-group/patchy/pkg/source"
)

// payloadSecret is the plaintext a real secret_scanning_alert delivery
// carries. The source must never read it.
const payloadSecret = "payload-plaintext-value-must-not-leak"

const secretCreatedPayload = `{
	"action": "created",
	"alert": {"number": 5, "secret_type": "slack_api_token", "secret": "` + payloadSecret + `"},
	"repository": {"name": "shop", "owner": {"login": "acme"}}
}`

type fakeSecretAlerts struct {
	alert *ghclient.SecretAlert
	err   error
	calls int
}

func (f *fakeSecretAlerts) GetSecretAlert(_ context.Context, _ ghclient.Repo, _ int) (*ghclient.SecretAlert, error) {
	f.calls++
	return f.alert, f.err
}

func testSecretAlert() *ghclient.SecretAlert {
	return &ghclient.SecretAlert{
		Number:                5,
		SecretType:            "slack_api_token",
		SecretTypeDisplayName: "Slack API Token",
		Provider:              "Slack",
		Validity:              "unknown",
		State:                 "open",
		HTMLURL:               "https://github.com/acme/shop/security/secret-scanning/5",
		Locations: []ghclient.SecretLocation{
			{Type: "commit", Path: "config/app.yaml", StartLine: 4, EndLine: 4, StartColumn: 9, EndColumn: 49,
				CommitSHA: "abc123"},
			{Type: "issue_comment", URL: "https://api.github.com/repos/acme/shop/issues/comments/1"},
		},
	}
}

func TestSecretsFindings(t *testing.T) {
	alerts := &fakeSecretAlerts{alert: testSecretAlert()}
	got, err := NewSecrets(alerts).Findings(context.Background(), "secret_scanning_alert", []byte(secretCreatedPayload))
	if err != nil {
		t.Fatalf("Findings() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Findings() = %d findings, want 1", len(got))
	}
	f := got[0]
	desc := f.Description
	f.Description = ""
	want := source.Finding{
		Source:      "ghas-secrets",
		Category:    source.CategorySecret,
		Repo:        source.Repo{Owner: "acme", Name: "shop"},
		AlertNumber: 5,
		Advisories:  []string{"secret:slack_api_token"},
		RuleID:      "slack_api_token",
		Title:       "Slack API Token exposed",
		Severity:    "high",
		HTMLURL:     "https://github.com/acme/shop/security/secret-scanning/5",
		Locations: []source.Location{{
			Path: "config/app.yaml", StartLine: 4, EndLine: 4, StartColumn: 9, EndColumn: 49,
		}},
	}
	if !reflect.DeepEqual(f, want) {
		t.Errorf("Findings() = %+v\nwant %+v", f, want)
	}
	for _, s := range []string{"Rotate or revoke", "`slack_api_token`", "`config/app.yaml:4`", "issue_comment"} {
		if !strings.Contains(desc, s) {
			t.Errorf("description missing %q:\n%s", s, desc)
		}
	}
	if strings.Contains(desc, payloadSecret) {
		t.Error("description carries the payload's secret value")
	}
}

func TestSecretsFindingsSkips(t *testing.T) {
	tests := []struct {
		name    string
		event   string
		payload string
	}{
		{"other event", "code_scanning_alert", secretCreatedPayload},
		{"resolved action", "secret_scanning_alert", `{"action":"resolved","alert":{"number":5},
			"repository":{"name":"shop","owner":{"login":"acme"}}}`},
		{"validated action", "secret_scanning_alert", `{"action":"validated","alert":{"number":5},
			"repository":{"name":"shop","owner":{"login":"acme"}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			alerts := &fakeSecretAlerts{alert: testSecretAlert()}
			got, err := NewSecrets(alerts).Findings(context.Background(), tt.event, []byte(tt.payload))
			if err != nil || got != nil {
				t.Errorf("Findings() = %v, %v; want nil, nil", got, err)
			}
			if alerts.calls != 0 {
				t.Errorf("GetSecretAlert called %d times for a skipped delivery", alerts.calls)
			}
		})
	}
}

func TestSecretsFindingsErrors(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		alerts  *fakeSecretAlerts
	}{
		{"bad json", "{nope", &fakeSecretAlerts{}},
		{"missing repo", `{"action":"created","alert":{"number":5}}`, &fakeSecretAlerts{}},
		{"fetch fails", secretCreatedPayload, &fakeSecretAlerts{err: errors.New("boom")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewSecrets(tt.alerts).Findings(context.Background(), "secret_scanning_alert",
				[]byte(tt.payload)); err == nil {
				t.Error("Findings() error = nil, want error")
			}
		})
	}
}

func TestSecretSeverity(t *testing.T) {
	tests := []struct {
		validity string
		public   bool
		want     string
	}{
		{"active", false, "critical"},
		{"inactive", true, "critical"},
		{"unknown", true, "critical"},
		{"inactive", false, "medium"},
		{"unknown", false, "high"},
		{"", false, "high"},
	}
	for _, tt := range tests {
		a := &ghclient.SecretAlert{Validity: tt.validity, PubliclyLeaked: tt.public}
		if got := secretSeverity(a); got != tt.want {
			t.Errorf("secretSeverity(validity %q, public %v) = %q, want %q", tt.validity, tt.public, got, tt.want)
		}
	}
}

type resolution struct {
	repo       ghclient.Repo
	number     int
	resolution string
	comment    string
}

type fakeSecretResolver struct {
	got []resolution
	err error
}

func (f *fakeSecretResolver) ResolveSecretAlert(
	_ context.Context, repo ghclient.Repo, number int, res, comment string,
) error {
	f.got = append(f.got, resolution{repo, number, res, comment})
	return f.err
}

func TestSecretsResolve(t *testing.T) {
	repo := ghclient.Repo{Owner: "acme", Name: "shop"}
	tests := []struct {
		reason string
		want   string
	}{
		{"", "false_positive"},
		{source.ReasonFalsePositive, "false_positive"},
		{source.ReasonUsedInTests, "used_in_tests"},
		{source.ReasonWontFix, "wont_fix"},
		{"something else", "false_positive"},
	}
	for _, tt := range tests {
		t.Run("reason "+tt.reason, func(t *testing.T) {
			r := &fakeSecretResolver{}
			v := source.Verdict{Kind: source.VerdictIgnore, Reason: tt.reason, Comment: "fixture"}
			if err := NewSecretsResolver(r, repo).Resolve(context.Background(),
				[]source.AlertRef{{ID: "5"}}, v); err != nil {
				t.Fatalf("Resolve() = %v", err)
			}
			want := []resolution{{repo, 5, tt.want, "fixture"}}
			if !reflect.DeepEqual(r.got, want) {
				t.Errorf("resolutions = %+v, want %+v", r.got, want)
			}
		})
	}

	t.Run("no-ops", func(t *testing.T) {
		for _, tt := range []struct {
			name    string
			repo    ghclient.Repo
			verdict source.Verdict
		}{
			{"repo-less finding", ghclient.Repo{}, source.Verdict{Kind: source.VerdictIgnore}},
			{"not an ignore", repo, source.Verdict{Kind: "remediate"}},
		} {
			r := &fakeSecretResolver{}
			if err := NewSecretsResolver(r, tt.repo).Resolve(context.Background(),
				[]source.AlertRef{{ID: "5"}}, tt.verdict); err != nil {
				t.Fatalf("%s: Resolve() = %v", tt.name, err)
			}
			if len(r.got) != 0 {
				t.Errorf("%s: resolutions = %+v, want none", tt.name, r.got)
			}
		}
	})

	t.Run("reports a bad id and failures", func(t *testing.T) {
		r := &fakeSecretResolver{err: errors.New("boom")}
		err := NewSecretsResolver(r, repo).Resolve(context.Background(),
			[]source.AlertRef{{ID: "x"}, {ID: "5"}}, source.Verdict{Kind: source.VerdictIgnore})
		if err == nil {
			t.Error("Resolve() = nil, want errors")
		}
		if len(r.got) != 1 {
			t.Errorf("resolutions = %+v, want alert 5 attempted", r.got)
		}
	})
}

type fakeSecretEnumerator struct {
	repos  []ghclient.Repo
	alert  *ghclient.SecretAlert
	filter []string
}

func (f *fakeSecretEnumerator) Enumerate(
	_ context.Context, repos []string, yield func(ghclient.Repo, *ghclient.SecretAlert) bool,
) (bool, error) {
	f.filter = repos
	for _, r := range f.repos {
		if !yield(r, f.alert) {
			return false, nil
		}
	}
	return true, nil
}

// The backfill mapping is the webhook mapping.
func TestSecretsListMapsLikeFindings(t *testing.T) {
	repo := ghclient.Repo{Owner: "acme", Name: "shop"}
	other := ghclient.Repo{Owner: "other", Name: "x"}
	alerts := &fakeSecretAlerts{alert: testSecretAlert()}
	delivered, err := NewSecrets(alerts).Findings(context.Background(), "secret_scanning_alert",
		[]byte(secretCreatedPayload))
	if err != nil {
		t.Fatalf("Findings() error = %v", err)
	}

	e := &fakeSecretEnumerator{repos: []ghclient.Repo{repo, other}, alert: testSecretAlert()}
	var listed []source.Finding
	complete, err := NewSecretsLister(e).List(context.Background(), []string{"acme/"}, func(f source.Finding) bool {
		listed = append(listed, f)
		return true
	})
	if err != nil || !complete {
		t.Fatalf("List() = %v, %v; want complete", complete, err)
	}
	if !reflect.DeepEqual(listed, delivered) {
		t.Errorf("List() = %+v\nwant %+v", listed, delivered)
	}
}
