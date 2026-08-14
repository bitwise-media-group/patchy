// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/web/authz"
)

// fullConfigIntegration exercises every projected integration field.
func fullConfigIntegration() *v1alpha1.Integration {
	at := metav1.NewTime(testClock.Add(-time.Hour))
	return &v1alpha1.Integration{
		ObjectMeta: metav1.ObjectMeta{Name: "gh", Namespace: "patchy"},
		Spec: v1alpha1.IntegrationSpec{
			Provider:  v1alpha1.IntegrationProviderGitHub,
			SecretRef: &v1alpha1.LocalSecretReference{Name: "creds"},
			Interval:  metav1.Duration{Duration: 10 * time.Minute},
			Backfill: &v1alpha1.BackfillRequest{
				By: "dev@acme.test", At: metav1.NewTime(testClock), Repositories: []string{"acme/"},
			},
			GitHub: &v1alpha1.GitHubIntegration{
				Issues:             &v1alpha1.GitHubIssues{Enabled: true},
				CodeScanningAlerts: &v1alpha1.GitHubCodeScanningAlerts{Enabled: true},
				Redelivery:         &v1alpha1.GitHubRedelivery{Enabled: true},
			},
		},
		Status: v1alpha1.IntegrationStatus{
			Conditions: []metav1.Condition{{
				Type: v1alpha1.ConditionReady, Status: metav1.ConditionTrue,
				Reason: "CredentialValid", LastTransitionTime: at,
			}},
			WebhookPath: "/github/webhooks",
			Redelivery: &v1alpha1.RedeliveryStatus{
				LastSweepAt: &at, Scanned: 12, Redelivered: 2, Truncated: true, Error: "one stuck",
			},
			Backfill: &v1alpha1.BackfillStatus{
				LastRunAt: &at, Listed: 40, Ingested: 39, Truncated: true, Error: "one bad",
				BackfilledAt: &at,
			},
		},
	}
}

func testForge() *v1alpha1.Forge {
	return &v1alpha1.Forge{
		ObjectMeta: metav1.ObjectMeta{Name: "github", Namespace: "patchy"},
		Spec: v1alpha1.ForgeSpec{
			Provider:     v1alpha1.ForgeProviderGitHub,
			SecretRef:    v1alpha1.LocalSecretReference{Name: "forge-creds"},
			Orgs:         []string{"acme"},
			Repositories: []string{"^acme/.*$"},
			Interval:     metav1.Duration{Duration: 10 * time.Minute},
		},
		Status: v1alpha1.ForgeStatus{
			Conditions: []metav1.Condition{{
				Type: v1alpha1.ConditionReady, Status: metav1.ConditionFalse,
				Reason: "CredentialInvalid", Message: "secret missing",
				LastTransitionTime: metav1.NewTime(testClock),
			}},
		},
	}
}

func getConfig(t *testing.T, s *Server) (*http.Response, string) {
	t.Helper()
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	res, err := http.Get(ts.URL + "/api/config")
	if err != nil {
		t.Fatalf("GET /api/config: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(res.Body)
	return res, strings.TrimSpace(string(body))
}

func TestHandleConfigGate(t *testing.T) {
	cases := []struct {
		name       string
		auth       stubAuth
		granter    stubGranter
		wantStatus int
	}{
		{"no session", stubAuth{}, stubGranter{}, http.StatusUnauthorized},
		{
			// View alone is not enough: the configuration surface has its
			// own gate (get on integrations), and is never public.
			"view without config grant",
			stubAuth{id: operator},
			stubGranter{grants: authz.Grants{View: true}},
			http.StatusForbidden,
		},
		{"granted", stubAuth{id: operator}, stubGranter{grants: allGrants()}, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := testServer(t, fullConfigIntegration())
			s.auth, s.granter = tc.auth, tc.granter
			res, body := getConfig(t, s)
			if res.StatusCode != tc.wantStatus {
				t.Errorf("status = %d (%s), want %d", res.StatusCode, body, tc.wantStatus)
			}
		})
	}
}

// fetchConfigDataset serves the standard two-integration fixture (gh +
// generic warehouse, one not-ready forge) through the full handler stack.
func fetchConfigDataset(t *testing.T) *ConfigDataset {
	t.Helper()
	generic := testIntegration("warehouse", func(i *v1alpha1.Integration) {
		i.Spec.Provider = v1alpha1.IntegrationProviderGeneric
		i.Spec.GitHub = nil
		i.Spec.Generic = &v1alpha1.GenericIntegration{
			Source:  &v1alpha1.GenericSource{Enabled: true},
			Enhance: &v1alpha1.GenericEnhancer{Enabled: true, URL: "https://wh.acme.test/enhance"},
		}
	})
	s := testServer(t, fullConfigIntegration(), generic, testForge())
	s.auth, s.granter = stubAuth{id: operator}, stubGranter{grants: allGrants()}

	res, body := getConfig(t, s)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d (%s)", res.StatusCode, body)
	}
	var ds ConfigDataset
	if err := json.Unmarshal([]byte(body), &ds); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(ds.Forges) != 1 || len(ds.Integrations) != 2 {
		t.Fatalf("forges/integrations = %d/%d, want 1/2", len(ds.Forges), len(ds.Integrations))
	}
	return &ds
}

func TestHandleConfigProjection(t *testing.T) {
	ds := fetchConfigDataset(t)
	forge := ds.Forges[0]
	if forge.Name != "github" || forge.Ready != "False" || forge.ReadyMessage != "secret missing" {
		t.Errorf("forge = %+v, want the not-ready condition surfaced", forge)
	}

	// Sorted by name: gh before warehouse.
	gh := ds.Integrations[0]
	if gh.Name != "gh" || !gh.BackfillSupported || gh.WebhookPath != "/github/webhooks" || gh.Ready != "True" {
		t.Errorf("integration = %+v", gh)
	}
	want := []string{"issues", "codeScanningAlerts", "redelivery"}
	if strings.Join(gh.Capabilities, ",") != strings.Join(want, ",") {
		t.Errorf("capabilities = %v, want %v", gh.Capabilities, want)
	}
	if gh.Redelivery == nil || !gh.Redelivery.Truncated || gh.Redelivery.Error != "one stuck" {
		t.Errorf("redelivery = %+v", gh.Redelivery)
	}
}

func TestHandleConfigBackfillBlock(t *testing.T) {
	ds := fetchConfigDataset(t)
	bf := ds.Integrations[0].Backfill
	if bf == nil || bf.Listed != 40 || bf.Ingested != 39 || !bf.Truncated || bf.Error != "one bad" {
		t.Fatalf("backfill = %+v", bf)
	}
	// The status echo predates the request stamp, so the request is pending.
	if !bf.Pending || bf.RequestedBy != "dev@acme.test" {
		t.Errorf("backfill pending/requestedBy = %v/%q, want pending by dev@acme.test",
			bf.Pending, bf.RequestedBy)
	}
}

func TestWireBackfillPending(t *testing.T) {
	req := metav1.NewTime(testClock)
	older := metav1.NewTime(testClock.Add(-time.Hour))
	cases := []struct {
		name    string
		status  *v1alpha1.BackfillStatus
		pending bool
	}{
		{"no status yet", nil, true},
		{"stale echoes only", &v1alpha1.BackfillStatus{BackfilledAt: &older, AttemptedAt: &older}, true},
		{"succeeded", &v1alpha1.BackfillStatus{BackfilledAt: &req, AttemptedAt: &req}, false},
		// A failed attempt clears pending even though the controller keeps
		// retrying — the trigger must re-enable for a corrected request.
		{"attempted and failed", &v1alpha1.BackfillStatus{AttemptedAt: &req, Error: "bad filter"}, false},
		// A status written before attemptedAt existed still clears on the
		// backfilledAt echo alone.
		{"legacy success echo", &v1alpha1.BackfillStatus{BackfilledAt: &req}, false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			integ := &v1alpha1.Integration{
				Spec:   v1alpha1.IntegrationSpec{Backfill: &v1alpha1.BackfillRequest{By: "dev", At: req}},
				Status: v1alpha1.IntegrationStatus{Backfill: tt.status},
			}
			if got := wireBackfill(integ).Pending; got != tt.pending {
				t.Errorf("wireBackfill().Pending = %v, want %v", got, tt.pending)
			}
		})
	}
}

func TestHandleConfigEnhancers(t *testing.T) {
	ds := fetchConfigDataset(t)
	if wh := ds.Integrations[1]; wh.BackfillSupported {
		t.Error("generic integration claims backfill support")
	}
	if len(ds.Enhancers) != 1 || ds.Enhancers[0].ID != "generic" || ds.Enhancers[0].Integration != "warehouse" {
		t.Errorf("enhancers = %+v, want the generic instance", ds.Enhancers)
	}
}

// Contract field names (ui/src/types.ts IntegrationConfig) on a fully
// populated integration.
func TestHandleConfigWireKeys(t *testing.T) {
	ds := fetchConfigDataset(t)
	m := asMap(t, ds.Integrations[0])
	for _, key := range []string{
		"name", "provider", "webhookPath", "secretRef", "interval",
		"ready", "capabilities", "redelivery", "backfill", "backfillSupported",
	} {
		if _, ok := m[key]; !ok {
			t.Errorf("integration config JSON is missing %q", key)
		}
	}
}

// A singleton enhancer capability enabled twice is surfaced as ambiguous on
// both rows, never silently reduced to one.
func TestDeriveEnhancersAmbiguous(t *testing.T) {
	mk := func(name string) v1alpha1.Integration {
		return v1alpha1.Integration{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "patchy"},
			Spec: v1alpha1.IntegrationSpec{
				Provider: v1alpha1.IntegrationProviderGoogleCloud,
				GoogleCloud: &v1alpha1.GoogleCloudIntegration{
					CloudAssetInventory: &v1alpha1.GoogleCloudAssetInventory{
						Enabled: true, Scope: "organizations/1",
					},
				},
			},
		}
	}
	out := deriveEnhancers([]v1alpha1.Integration{mk("gcp-a"), mk("gcp-b")})
	if len(out) != 2 {
		t.Fatalf("enhancers = %+v, want both instances surfaced", out)
	}
	for _, e := range out {
		if e.ID != "google-cloud-labels" || !e.Ambiguous {
			t.Errorf("enhancer = %+v, want google-cloud-labels marked ambiguous", e)
		}
	}
}

func postBackfill(t *testing.T, s *Server, name, body string) (*http.Response, string) {
	t.Helper()
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	res, err := http.Post(ts.URL+"/api/integrations/"+name+"/actions/backfill", "application/json",
		strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	return res, strings.TrimSpace(string(raw))
}

func TestHandleBackfill(t *testing.T) {
	supported := func(i *v1alpha1.Integration) {
		i.Spec.GitHub.CodeScanningAlerts = &v1alpha1.GitHubCodeScanningAlerts{Enabled: true}
	}
	cases := []struct {
		name       string
		target     string
		body       string
		grants     authz.Grants
		mutate     []func(*v1alpha1.Integration)
		wantStatus int
	}{
		{
			name: "granted with filter", target: "gh", body: `{"repositories":["acme/","acme/shop"]}`,
			grants: allGrants(), mutate: []func(*v1alpha1.Integration){supported},
			wantStatus: http.StatusOK,
		},
		{
			name: "granted without body", target: "gh", body: "",
			grants: allGrants(), mutate: []func(*v1alpha1.Integration){supported},
			wantStatus: http.StatusOK,
		},
		{
			name: "verb not granted", target: "gh", body: "{}",
			grants: authz.Grants{View: true, Config: true, Integration: []string{authz.VerbReplay}},
			mutate: []func(*v1alpha1.Integration){supported}, wantStatus: http.StatusForbidden,
		},
		{
			name: "unknown integration", target: "nope", body: "{}",
			grants: allGrants(), mutate: []func(*v1alpha1.Integration){supported},
			wantStatus: http.StatusNotFound,
		},
		{
			name: "suspended", target: "gh", body: "{}",
			grants: allGrants(),
			mutate: []func(*v1alpha1.Integration){supported, func(i *v1alpha1.Integration) {
				i.Spec.Suspend = true
			}},
			wantStatus: http.StatusConflict,
		},
		{
			name: "unsupported capability", target: "gh", body: "{}",
			grants:     allGrants(),
			wantStatus: http.StatusConflict,
		},
		{
			name: "malformed body", target: "gh", body: "{not json",
			grants: allGrants(), mutate: []func(*v1alpha1.Integration){supported},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "whitespace filter entry", target: "gh", body: `{"repositories":["acme /shop"]}`,
			grants: allGrants(), mutate: []func(*v1alpha1.Integration){supported},
			wantStatus: http.StatusBadRequest,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := testServer(t, testIntegration("gh", tc.mutate...))
			s.auth, s.granter = stubAuth{id: operator}, stubGranter{grants: tc.grants}
			res, body := postBackfill(t, s, tc.target, tc.body)
			if res.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d (%s), want %d", res.StatusCode, body, tc.wantStatus)
			}
			if tc.wantStatus != http.StatusOK {
				return
			}
			var integ v1alpha1.Integration
			key := types.NamespacedName{Namespace: "patchy", Name: "gh"}
			if err := mustClient(s).Get(t.Context(), key, &integ); err != nil {
				t.Fatalf("get integration: %v", err)
			}
			req := integ.Spec.Backfill
			if req == nil || req.By != "op@acme.test" || !req.At.Time.Equal(testClock) {
				t.Fatalf("spec.backfill = %+v, want stamped by op@acme.test", req)
			}
		})
	}
}
