// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package integration

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/generic"
	"github.com/bitwise-media-group/patchy/internal/ghsecret"
	"github.com/bitwise-media-group/patchy/internal/kube"
	"github.com/bitwise-media-group/patchy/internal/webhook"
	"github.com/bitwise-media-group/patchy/pkg/source"
)

// genericFindingsJSON is one findings delivery per the pkg/generic contract.
const genericFindingsJSON = `{
	"version": "v1",
	"event": "findings",
	"findings": [{
		"repo": {"owner": "bitwise-media-group", "name": "shop"},
		"alertId": "wh-1001",
		"advisories": ["CVE-2026-1234"],
		"title": "SQL injection in the order lookup",
		"severity": "high",
		"htmlUrl": "https://warehouse.internal/findings/1001"
	}]
}`

func testGenericIntegration(name string, mutate ...func(*v1alpha1.GenericIntegration)) *v1alpha1.Integration {
	g := &v1alpha1.GenericIntegration{
		Source: &v1alpha1.GenericSource{Enabled: true, MinSeverity: v1alpha1.LevelLow},
	}
	for _, m := range mutate {
		m(g)
	}
	return &v1alpha1.Integration{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "patchy"},
		Spec: v1alpha1.IntegrationSpec{
			Provider:  v1alpha1.IntegrationProviderGeneric,
			SecretRef: &v1alpha1.LocalSecretReference{Name: name + "-creds"},
			Generic:   g,
		},
	}
}

func genericSecret(name, secret string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name + "-creds", Namespace: "patchy"},
		Data:       map[string][]byte{ghsecret.KeyWebhookSecret: []byte(secret)},
	}
}

func TestGenericSecretsFor(t *testing.T) {
	r := newWizReceiver(t,
		testGenericIntegration("warehouse"), genericSecret("warehouse", "wh-secret"),
		testGenericIntegration("cmdb"), genericSecret("cmdb", "cmdb-secret"))

	secretsFor := func(name string) [][]byte {
		req := httptest.NewRequest("POST", "/generic/"+name+"/webhooks", nil)
		req.SetPathValue("name", name)
		return r.genericSecretsFor(t.Context(), req)
	}

	if got := secretsFor("warehouse"); len(got) != 1 || string(got[0]) != "wh-secret" {
		t.Errorf("genericSecretsFor(warehouse) = %q, want exactly its own secret", got)
	}
	if got := secretsFor("cmdb"); len(got) != 1 || string(got[0]) != "cmdb-secret" {
		t.Errorf("genericSecretsFor(cmdb) = %q, want exactly its own secret", got)
	}
	if got := secretsFor("unknown"); len(got) != 0 {
		t.Errorf("genericSecretsFor(unknown) = %d candidates, want none", len(got))
	}
}

// A suspended integration, a non-generic one sharing the name space, and an
// enhance-only one all yield no candidates: the route fails closed.
func TestGenericSecretsForRefusesUnusable(t *testing.T) {
	suspended := testGenericIntegration("suspended")
	suspended.Spec.Suspend = true
	enhanceOnly := testGenericIntegration("enhance-only", func(g *v1alpha1.GenericIntegration) {
		g.Source = nil
		g.Enhance = &v1alpha1.GenericEnhancer{Enabled: true, URL: "https://cmdb.internal/enhance"}
	})
	r := newWizReceiver(t,
		suspended, genericSecret("suspended", "s"),
		enhanceOnly, genericSecret("enhance-only", "s"),
		testIntegration())

	for _, name := range []string{"suspended", "enhance-only", "gh", ""} {
		req := httptest.NewRequest("POST", "/generic/x/webhooks", nil)
		req.SetPathValue("name", name)
		if got := r.genericSecretsFor(t.Context(), req); len(got) != 0 {
			t.Errorf("genericSecretsFor(%q) = %d candidates, want none", name, len(got))
		}
	}
}

func TestHandleGenericIngests(t *testing.T) {
	r := newWizReceiver(t, testGenericIntegration("warehouse"), genericSecret("warehouse", "s"))
	e := webhook.Event{
		Type:    generic.EventFindings,
		Payload: []byte(genericFindingsJSON),
		Path:    GenericPathFor("warehouse"),
	}
	if err := r.handleGeneric(t.Context(), e); err != nil {
		t.Fatalf("handleGeneric() = %v, want nil", err)
	}
	items := listFindings(t, r.Ingest.Client)
	if len(items) != 1 {
		t.Fatalf("ingested %d findings, want 1", len(items))
	}
	f := items[0]
	if f.Spec.Source != "warehouse" {
		t.Errorf("spec.source = %q, want the integration name", f.Spec.Source)
	}
	if f.Spec.Repository == nil || f.Spec.Repository.Name != "bitwise-media-group/shop" {
		t.Errorf("spec.repository = %+v, want the delivered repo", f.Spec.Repository)
	}
	if len(f.Spec.Advisories) == 0 || f.Spec.Advisories[0] != "CVE-2026-1234" {
		t.Errorf("advisories = %v, want the delivered ordering", f.Spec.Advisories)
	}
}

// Two generic integrations ingest independently: each delivery lands under
// its own path and its findings carry its own name as the source.
func TestHandleGenericTwoIntegrations(t *testing.T) {
	r := newWizReceiver(t,
		testGenericIntegration("warehouse"), genericSecret("warehouse", "s1"),
		testGenericIntegration("scanner-x"), genericSecret("scanner-x", "s2"))
	for _, name := range []string{"warehouse", "scanner-x"} {
		e := webhook.Event{
			Type:    generic.EventFindings,
			Payload: []byte(genericFindingsJSON),
			Path:    GenericPathFor(name),
		}
		if err := r.handleGeneric(t.Context(), e); err != nil {
			t.Fatalf("handleGeneric(%s) = %v, want nil", name, err)
		}
	}
	items := listFindings(t, r.Ingest.Client)
	if len(items) != 2 {
		t.Fatalf("ingested %d findings, want 2 (one per integration)", len(items))
	}
	sources := map[string]bool{}
	for _, f := range items {
		sources[f.Spec.Source] = true
	}
	if !sources["warehouse"] || !sources["scanner-x"] {
		t.Errorf("sources = %v, want one finding per integration name", sources)
	}
}

// An integration removed (or disabled) between authentication and handling
// drops the delivery silently, like every other unconfigured route.
func TestHandleGenericGoneIntegration(t *testing.T) {
	r := newWizReceiver(t)
	e := webhook.Event{
		Type:    generic.EventFindings,
		Payload: []byte(genericFindingsJSON),
		Path:    GenericPathFor("warehouse"),
	}
	if err := r.handleGeneric(t.Context(), e); err != nil {
		t.Fatalf("handleGeneric(gone integration) = %v, want nil", err)
	}
	if items := listFindings(t, r.Ingest.Client); len(items) != 0 {
		t.Errorf("ingested %d findings without an integration, want none", len(items))
	}
}

// The severity floor flows from the Integration spec into the handler.
func TestHandleGenericMinSeverity(t *testing.T) {
	floor := testGenericIntegration("warehouse", func(g *v1alpha1.GenericIntegration) {
		g.Source.MinSeverity = v1alpha1.LevelCritical
	})
	r := newWizReceiver(t, floor, genericSecret("warehouse", "s"))
	e := webhook.Event{
		Type:    generic.EventFindings,
		Payload: []byte(genericFindingsJSON),
		Path:    GenericPathFor("warehouse"),
	}
	if err := r.handleGeneric(t.Context(), e); err != nil {
		t.Fatalf("handleGeneric() = %v, want nil", err)
	}
	if items := listFindings(t, r.Ingest.Client); len(items) != 0 {
		t.Errorf("ingested %d findings below the severity floor, want none", len(items))
	}
}

func TestGenericDecoder(t *testing.T) {
	req := httptest.NewRequest("POST", "/generic/warehouse/webhooks", nil)
	event, id, err := genericDecoder(req, []byte(genericFindingsJSON))
	if err != nil {
		t.Fatalf("genericDecoder() = %v, want nil", err)
	}
	if event != generic.EventFindings {
		t.Errorf("event = %q, want %q", event, generic.EventFindings)
	}
	if len(id) != 16 {
		t.Errorf("delivery id = %q, want a 16-char digest", id)
	}
	if event, _, err := genericDecoder(req, []byte(`{"version":"v1","event":"ping"}`)); err != nil || event != "ping" {
		t.Errorf("genericDecoder(ping) = (%q, %v), want (ping, nil)", event, err)
	}
	if _, _, err := genericDecoder(req, []byte("not json")); err == nil {
		t.Error("genericDecoder(undecodable) = nil error, want 400-causing error")
	}
}

func TestValidateGeneric(t *testing.T) {
	for _, tt := range []struct {
		name    string
		integ   *v1alpha1.Integration
		secret  *corev1.Secret
		wantErr string
	}{
		{"valid source", testGenericIntegration("warehouse"), genericSecret("warehouse", "s"), ""},
		{
			"valid enhance-only",
			testGenericIntegration("cmdb", func(g *v1alpha1.GenericIntegration) {
				g.Source = nil
				g.Enhance = &v1alpha1.GenericEnhancer{Enabled: true, URL: "https://cmdb.internal/enhance"}
			}),
			genericSecret("cmdb", "s"),
			"",
		},
		{
			"no capability",
			testGenericIntegration("idle", func(g *v1alpha1.GenericIntegration) { g.Source.Enabled = false }),
			genericSecret("idle", "s"),
			"enables no capability",
		},
		{
			"missing webhook secret key",
			testGenericIntegration("warehouse"),
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "warehouse-creds", Namespace: "patchy"}},
			"missing key webhookSecret",
		},
		{
			"reserved name",
			testGenericIntegration("ghas"),
			genericSecret("ghas", "s"),
			"reserved source id",
		},
		{
			"resolver without url",
			testGenericIntegration("warehouse", func(g *v1alpha1.GenericIntegration) {
				g.Source.Resolver = &v1alpha1.GenericResolver{Enabled: true}
			}),
			genericSecret("warehouse", "s"),
			"without a url",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(kube.Scheme()).WithObjects(tt.secret).Build()
			err := NewCreds(c).Validate(t.Context(), tt.integ)
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Validate() = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

// fakeGenericResolver records write-back calls.
type fakeGenericResolver struct {
	calls   int
	alerts  []source.AlertRef
	verdict source.Verdict
}

func (f *fakeGenericResolver) Resolve(_ context.Context, alerts []source.AlertRef, v source.Verdict) error {
	f.calls++
	f.alerts = alerts
	f.verdict = v
	return nil
}

// dismissedGenericFinding is a dismissed Finding whose alerts came from the
// named generic integration, with no tracking issue in the way.
func dismissedGenericFinding(name string) *v1alpha1.Finding {
	fnd := projectable(v1alpha1.PhaseDismissed)
	fnd.Spec.IntegrationRef = v1alpha1.LocalObjectReference{Name: name}
	fnd.Spec.TrackingRef = nil
	fnd.Spec.Source = name
	fnd.Spec.Alerts = []v1alpha1.Alert{
		{ID: "wh-1001", Source: name, URL: "https://warehouse.internal/findings/1001"},
	}
	return fnd
}

func TestResolveGenericSource(t *testing.T) {
	integ := testGenericIntegration("warehouse", func(g *v1alpha1.GenericIntegration) {
		g.Source.Resolver = &v1alpha1.GenericResolver{Enabled: true, URL: "https://warehouse.internal/resolve"}
	})
	r, c := newProjector(t, newFakeTracker(), integ, dismissedGenericFinding("warehouse"))
	resolver := &fakeGenericResolver{}
	r.GenericResolver = func(_ context.Context, got *v1alpha1.Integration) (source.Resolver, error) {
		if got.Name != "warehouse" {
			t.Errorf("resolver built for %q, want warehouse", got.Name)
		}
		return resolver, nil
	}

	reconcileFinding(t, r)
	reconcileFinding(t, r) // the annotation makes the write-back once-only

	if resolver.calls != 1 {
		t.Fatalf("resolver called %d times, want exactly 1", resolver.calls)
	}
	if len(resolver.alerts) != 1 || resolver.alerts[0].ID != "wh-1001" {
		t.Errorf("alerts = %+v, want the finding's alert", resolver.alerts)
	}
	if resolver.verdict.Kind != source.VerdictIgnore {
		t.Errorf("verdict = %+v, want ignore", resolver.verdict)
	}
	f := get(t, c, "finding-aa-1")
	if f.GetAnnotations()[AnnotationResolvedSource] != string(v1alpha1.PhaseDismissed) {
		t.Errorf("resolved-source annotation = %q, want the phase stamped", f.GetAnnotations()[AnnotationResolvedSource])
	}
}

// A generic source with the resolver off — or gone entirely — is a complete
// source: the dismissal proceeds with no write-back and no error.
func TestResolveGenericSourceWithoutResolver(t *testing.T) {
	for _, tt := range []struct {
		name string
		objs []client.Object
	}{
		{"resolver disabled", []client.Object{testGenericIntegration("warehouse")}},
		{"integration gone", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			objs := append(tt.objs, client.Object(dismissedGenericFinding("warehouse")))
			r, c := newProjector(t, newFakeTracker(), objs...)
			r.GenericResolver = func(context.Context, *v1alpha1.Integration) (source.Resolver, error) {
				t.Fatal("resolver built for a source with write-back off")
				return nil, nil
			}
			reconcileFinding(t, r)
			f := get(t, c, "finding-aa-1")
			if f.GetAnnotations()[AnnotationResolvedSource] != string(v1alpha1.PhaseDismissed) {
				t.Error("dismissal not marked resolved; a resolver-less source must not wedge")
			}
		})
	}
}

func TestWebhookPath(t *testing.T) {
	if got := webhookPath(testGenericIntegration("warehouse")); got != "/generic/warehouse/webhooks" {
		t.Errorf("webhookPath(generic) = %q, want the per-name path", got)
	}
	if got := webhookPath(testIntegration()); got != GitHubPath {
		t.Errorf("webhookPath(github) = %q, want %q", got, GitHubPath)
	}
}
