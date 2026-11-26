// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
		},
	}
}

// testBackfillRequest builds a BackfillRequest on gh created at the given
// offset from testClock.
func testBackfillRequest(
	name string, created time.Duration, mutate ...func(*v1alpha1.BackfillRequest),
) *v1alpha1.BackfillRequest {
	b := &v1alpha1.BackfillRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "patchy", CreationTimestamp: metav1.NewTime(testClock.Add(created)),
		},
		Spec: v1alpha1.BackfillRequestSpec{
			IntegrationRef: v1alpha1.LocalObjectReference{Name: "gh"},
			RequestedBy:    "dev@acme.test",
		},
	}
	for _, m := range mutate {
		m(b)
	}
	return b
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
			// own gate (get on integrations).
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
	at := metav1.NewTime(testClock.Add(-time.Hour))
	// The latest request on gh is the newest one an operator has not left
	// waiting; an older finished one is superseded.
	older := testBackfillRequest("backfill-old", -2*time.Hour, func(b *v1alpha1.BackfillRequest) {
		b.Status = v1alpha1.BackfillRequestStatus{Phase: v1alpha1.BackfillComplete, LastAttemptAt: &at, CompletedAt: &at}
	})
	latest := testBackfillRequest("backfill-new", 0, func(b *v1alpha1.BackfillRequest) {
		b.Spec.Repositories = []string{"acme/"}
		b.Status = v1alpha1.BackfillRequestStatus{
			Phase: v1alpha1.BackfillPending, LastAttemptAt: nil, Listed: 40, Ingested: 39, Truncated: true, Error: "one bad",
		}
	})
	waiting := testBackfillRequest("backfill-waiting", time.Minute, func(b *v1alpha1.BackfillRequest) {
		b.Spec.Via = v1alpha1.BackfillViaRequested
	})
	s := testServer(t, fullConfigIntegration(), generic, testForge(), older, latest, waiting)
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
	if bf == nil || bf.Request != "backfill-new" || bf.Listed != 40 || bf.Ingested != 39 || !bf.Truncated ||
		bf.Error != "one bad" {
		t.Fatalf("backfill = %+v, want the newest decided request", bf)
	}
	// Not attempted yet, so the trigger stays disabled.
	if !bf.Pending || bf.Phase != "Pending" || bf.RequestedBy != "dev@acme.test" {
		t.Errorf("backfill pending/phase/requestedBy = %v/%s/%q, want pending by dev@acme.test",
			bf.Pending, bf.Phase, bf.RequestedBy)
	}
}

func TestWireBackfillPending(t *testing.T) {
	cases := []struct {
		name    string
		status  v1alpha1.BackfillRequestStatus
		pending bool
	}{
		{"not seen yet", v1alpha1.BackfillRequestStatus{}, true},
		{"held by its integration", v1alpha1.BackfillRequestStatus{
			Phase: v1alpha1.BackfillPending, Error: "suspended",
		}, true},
		// A failed attempt clears pending even though the controller keeps
		// retrying; the trigger must re-enable for a corrected request.
		{"attempted and failed", v1alpha1.BackfillRequestStatus{
			Phase: v1alpha1.BackfillPending, Attempts: 1, Error: "bad filter",
		}, false},
		{"complete", v1alpha1.BackfillRequestStatus{Phase: v1alpha1.BackfillComplete, Attempts: 1}, false},
		{"failed", v1alpha1.BackfillRequestStatus{Phase: v1alpha1.BackfillFailed, Attempts: 5}, false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			b := testBackfillRequest("b", 0, func(b *v1alpha1.BackfillRequest) { b.Status = tt.status })
			if got := wireBackfill(b).Pending; got != tt.pending {
				t.Errorf("wireBackfill().Pending = %v, want %v", got, tt.pending)
			}
		})
	}
	if wireBackfill(nil) != nil {
		t.Error("no request projected a backfill block")
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
			var list v1alpha1.BackfillRequestList
			if err := mustClient(s).List(t.Context(), &list); err != nil {
				t.Fatalf("list backfill requests: %v", err)
			}
			if len(list.Items) != 1 {
				t.Fatalf("backfill requests = %d, want 1", len(list.Items))
			}
			req := list.Items[0]
			if req.Spec.RequestedBy != "op@acme.test" || req.Spec.IntegrationRef.Name != "gh" || req.Spec.Via != "" ||
				req.Labels[backfillIntegrationLabel] != "gh" || !strings.Contains(body, req.Name) {
				t.Fatalf("request = %+v (answer %s), want one by op@acme.test on gh", req, body)
			}
		})
	}
}

func TestBackfillSupportedFollowsScanningCapabilities(t *testing.T) {
	gh := func(code, secrets bool) *v1alpha1.Integration {
		i := &v1alpha1.Integration{Spec: v1alpha1.IntegrationSpec{
			Provider: v1alpha1.IntegrationProviderGitHub,
			GitHub: &v1alpha1.GitHubIntegration{
				CodeScanningAlerts:   &v1alpha1.GitHubCodeScanningAlerts{Enabled: code},
				SecretScanningAlerts: &v1alpha1.GitHubSecretScanningAlerts{Enabled: secrets},
			},
		}}
		return i
	}
	cases := []struct {
		name  string
		integ *v1alpha1.Integration
		want  bool
	}{
		{"code scanning", gh(true, false), true},
		{"secret scanning", gh(false, true), true},
		{"neither", gh(false, false), false},
		{"not github", &v1alpha1.Integration{Spec: v1alpha1.IntegrationSpec{
			Provider: v1alpha1.IntegrationProviderWiz}}, false},
	}
	for _, tc := range cases {
		if got := backfillSupported(tc.integ); got != tc.want {
			t.Errorf("%s: backfillSupported() = %v, want %v", tc.name, got, tc.want)
		}
	}
	if caps := capabilities(gh(false, true)); !slices.Contains(caps, "secretScanningAlerts") {
		t.Errorf("capabilities = %v, want secretScanningAlerts", caps)
	}
}
