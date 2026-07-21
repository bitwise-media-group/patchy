// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package integration

import (
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/kube"
	"github.com/bitwise-media-group/patchy/pkg/source"
)

var testClock = time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)

func testIntegration() *v1alpha1.Integration {
	return &v1alpha1.Integration{
		ObjectMeta: metav1.ObjectMeta{Name: "gh", Namespace: "patchy"},
		Spec: v1alpha1.IntegrationSpec{
			Provider:  v1alpha1.IntegrationProviderGitHub,
			SecretRef: v1alpha1.LocalSecretReference{Name: "creds"},
			GitHub: &v1alpha1.GitHubIntegration{
				Issues:             &v1alpha1.GitHubIssues{Enabled: true},
				CodeScanningAlerts: &v1alpha1.GitHubCodeScanningAlerts{Enabled: true},
			},
		},
	}
}

func testSourceFinding(alert int) source.Finding {
	return source.Finding{
		Source:      "ghas",
		Repo:        source.Repo{Owner: "acme", Name: "orders"},
		AlertNumber: alert,
		Advisories:  []string{"CVE-2026-0001", "CWE-79"},
		RuleID:      "js/xss",
		Title:       "Reflected XSS",
		Description: "desc",
		Severity:    "high",
		HTMLURL:     "https://github.com/acme/orders/security/code-scanning/42",
		Locations:   []source.Location{{Path: "src/app.js", StartLine: 10, EndLine: 12}},
	}
}

func newIngestor(t *testing.T) (*Ingestor, client.Client) {
	t.Helper()
	c := fake.NewClientBuilder().
		WithScheme(kube.Scheme()).
		WithStatusSubresource(&v1alpha1.Finding{}).
		Build()
	return &Ingestor{
		Client:    c,
		Namespace: "patchy",
		Window:    time.Hour,
		Now:       func() time.Time { return testClock },
	}, c
}

func listFindings(t *testing.T, c client.Client) []v1alpha1.Finding {
	t.Helper()
	var list v1alpha1.FindingList
	if err := c.List(t.Context(), &list, client.InNamespace("patchy")); err != nil {
		t.Fatalf("List: %v", err)
	}
	return list.Items
}

func TestIngestCreatesFinding(t *testing.T) {
	in, c := newIngestor(t)
	if err := in.Ingest(t.Context(), testIntegration(), testSourceFinding(42)); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	items := listFindings(t, c)
	if len(items) != 1 {
		t.Fatalf("findings = %d, want 1", len(items))
	}
	f := items[0]
	if f.Labels[v1alpha1.LabelKeyHash] == "" || f.Labels[v1alpha1.LabelSeverity] != "high" {
		t.Errorf("labels = %v, want key-hash and severity", f.Labels)
	}
	if f.Spec.Repository == nil || f.Spec.Repository.URL != "https://github.com/acme/orders" {
		t.Errorf("repository = %+v", f.Spec.Repository)
	}
	if len(f.Spec.Alerts) != 1 || f.Spec.Alerts[0].ID != "42" {
		t.Errorf("alerts = %+v, want one alert id 42", f.Spec.Alerts)
	}
	if f.Status.Phase != v1alpha1.PhaseOpened {
		t.Errorf("phase = %q, want Opened", f.Status.Phase)
	}
	if f.Status.AccumulateUntil == nil || !f.Status.AccumulateUntil.Time.Equal(testClock.Add(time.Hour)) {
		t.Errorf("accumulateUntil = %v, want +1h", f.Status.AccumulateUntil)
	}
	if f.Spec.TrackingRef == nil || f.Spec.TrackingRef.Name != "gh" {
		t.Errorf("trackingRef = %+v, want gh", f.Spec.TrackingRef)
	}
}

func TestIngestFoldsAndDedups(t *testing.T) {
	in, c := newIngestor(t)
	integ := testIntegration()
	for _, alert := range []int{42, 43, 43} {
		if err := in.Ingest(t.Context(), integ, testSourceFinding(alert)); err != nil {
			t.Fatalf("Ingest(%d): %v", alert, err)
		}
	}
	items := listFindings(t, c)
	if len(items) != 1 {
		t.Fatalf("findings = %d, want 1 (accumulated)", len(items))
	}
	if len(items[0].Spec.Alerts) != 2 {
		t.Errorf("alerts = %+v, want 2 (43 deduped)", items[0].Spec.Alerts)
	}
}

func TestIngestOpensSuccessorGeneration(t *testing.T) {
	in, c := newIngestor(t)
	integ := testIntegration()
	if err := in.Ingest(t.Context(), integ, testSourceFinding(42)); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	// Advance generation 1 past the foldable phases.
	first := listFindings(t, c)[0]
	first.Status.Phase = v1alpha1.PhaseInvestigating
	if err := c.Status().Update(t.Context(), &first); err != nil {
		t.Fatalf("advance phase: %v", err)
	}

	if err := in.Ingest(t.Context(), integ, testSourceFinding(99)); err != nil {
		t.Fatalf("Ingest successor: %v", err)
	}
	items := listFindings(t, c)
	if len(items) != 2 {
		t.Fatalf("findings = %d, want 2 generations", len(items))
	}
	var gen2 *v1alpha1.Finding
	for i := range items {
		if generationOf(items[i].Name) == 2 {
			gen2 = &items[i]
		}
	}
	if gen2 == nil {
		t.Fatalf("no generation-2 finding in %v", names(items))
	}
	if len(gen2.Spec.Related) != 1 || gen2.Spec.Related[0].Relationship != v1alpha1.RelationshipSuccessorOf {
		t.Errorf("related = %+v, want successor-of edge", gen2.Spec.Related)
	}
	// Elder carries the mirrored edge.
	var elder v1alpha1.Finding
	elderKey := types.NamespacedName{Namespace: "patchy", Name: gen2.Spec.Related[0].To}
	if err := c.Get(t.Context(), elderKey, &elder); err != nil {
		t.Fatalf("get elder: %v", err)
	}
	if len(elder.Spec.Related) != 1 {
		t.Errorf("elder related = %+v, want mirrored edge", elder.Spec.Related)
	}
}

func TestIngestAlertOverflow(t *testing.T) {
	in, c := newIngestor(t)
	integ := testIntegration()
	for alert := range maxAlerts + 3 {
		if err := in.Ingest(t.Context(), integ, testSourceFinding(alert)); err != nil {
			t.Fatalf("Ingest(%d): %v", alert, err)
		}
	}
	f := listFindings(t, c)[0]
	if len(f.Spec.Alerts) != maxAlerts {
		t.Errorf("alerts = %d, want capped at %d", len(f.Spec.Alerts), maxAlerts)
	}
	if f.Spec.OverflowAlerts != 3 {
		t.Errorf("overflowAlerts = %d, want 3", f.Spec.OverflowAlerts)
	}
}

func names(items []v1alpha1.Finding) []string {
	out := make([]string, len(items))
	for i := range items {
		out[i] = items[i].Name
	}
	return out
}

func TestAccumulationConditionFlips(t *testing.T) {
	in, c := newIngestor(t)
	if err := in.Ingest(t.Context(), testIntegration(), testSourceFinding(42)); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	name := listFindings(t, c)[0].Name

	r := &FindingReconciler{Client: c, Namespace: "patchy", Now: func() time.Time { return testClock }}
	// Window still open: requeue for the remainder, condition not set.
	wait, err := r.closeAccumulation(t.Context(), get(t, c, name))
	if err != nil || wait != time.Hour {
		t.Fatalf("closeAccumulation = (%v, %v), want (1h, nil)", wait, err)
	}
	// Window elapsed.
	r.Now = func() time.Time { return testClock.Add(2 * time.Hour) }
	if _, err := r.closeAccumulation(t.Context(), get(t, c, name)); err != nil {
		t.Fatalf("closeAccumulation elapsed: %v", err)
	}
	f := get(t, c, name)
	if !meta.IsStatusConditionTrue(f.Status.Conditions, v1alpha1.ConditionAccumulationComplete) {
		t.Errorf("AccumulationComplete = %+v, want True", f.Status.Conditions)
	}
}

func get(t *testing.T, c client.Client, name string) *v1alpha1.Finding {
	t.Helper()
	var f v1alpha1.Finding
	if err := c.Get(t.Context(), types.NamespacedName{Namespace: "patchy", Name: name}, &f); err != nil {
		t.Fatalf("Get(%s): %v", name, err)
	}
	return &f
}
