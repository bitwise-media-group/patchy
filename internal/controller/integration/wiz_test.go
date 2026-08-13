// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package integration

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/kube"
	"github.com/bitwise-media-group/patchy/internal/webhook"
	"github.com/bitwise-media-group/patchy/internal/wiz"
)

// wizIssueJSON is a Wiz Issues delivery per the documented body template,
// trimmed to what routing and ingest read.
const wizIssueJSON = `{
	"trigger": {"source": "ISSUE", "type": "Created", "ruleId": "ar-1", "ruleName": "patchy-issues"},
	"issue": {
		"id": "iss-1", "status": "OPEN", "severity": "HIGH",
		"url": "https://app.wiz.io/issues#~(issue~'iss-1')",
		"control": {"id": "wc-1", "name": "Public bucket"}
	},
	"entitySnapshot": {
		"type": "BUCKET", "nativeType": "storage#bucket", "name": "acme-artifacts",
		"cloudPlatform": "GCP",
		"providerId": "https://www.googleapis.com/storage/v1/b/acme-artifacts",
		"region": "europe-west2", "subscriptionExternalId": "acme-prod"
	}
}`

// wizThreatJSON is a Wiz Defend delivery per the documented template.
const wizThreatJSON = `{
	"trigger": {"source": "THREAT_DETECTION", "type": "Created"},
	"threat": {
		"id": "t-1", "name": "Suspicious key usage", "severity": "CRITICAL", "status": "OPEN",
		"ruleId": "dr-1", "ruleName": "Unusual key usage", "cloudPlatform": "GCP",
		"resources": [{
			"id": "r-1", "name": "build-vm", "nativeType": "compute#instance",
			"providerId": "//compute.googleapis.com/projects/p/zones/z/instances/build-vm",
			"cloudPlatform": "GCP", "subscriptionExternalId": "p"
		}]
	}
}`

func testWizIntegration(issues, defend bool) *v1alpha1.Integration {
	integ := &v1alpha1.Integration{
		ObjectMeta: metav1.ObjectMeta{Name: "wiz", Namespace: "patchy"},
		Spec: v1alpha1.IntegrationSpec{
			Provider:  v1alpha1.IntegrationProviderWiz,
			SecretRef: &v1alpha1.LocalSecretReference{Name: "wiz-creds"},
			Wiz:       &v1alpha1.WizIntegration{},
		},
	}
	if issues {
		integ.Spec.Wiz.Issues = &v1alpha1.WizIssues{Enabled: true, MinSeverity: v1alpha1.LevelLow}
	}
	if defend {
		integ.Spec.Wiz.Defend = &v1alpha1.WizDefend{Enabled: true, MinSeverity: v1alpha1.LevelLow}
	}
	return integ
}

func newWizReceiver(t *testing.T, objs ...client.Object) *Receiver {
	t.Helper()
	c := fake.NewClientBuilder().
		WithScheme(kube.Scheme()).
		WithStatusSubresource(&v1alpha1.Finding{}).
		WithObjects(objs...).
		WithIndex(&v1alpha1.Finding{}, KeyHashIndex, KeyHashIndexer).
		Build()
	return &Receiver{
		Reader:    c,
		Creds:     NewCreds(c),
		Ingest:    &Ingestor{Client: c, Namespace: "patchy"},
		Namespace: "patchy",
	}
}

func wizSecret(token string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "wiz-creds", Namespace: "patchy"},
		Data:       map[string][]byte{wiz.KeyWebhookToken: []byte(token)},
	}
}

func TestWizTokens(t *testing.T) {
	r := newWizReceiver(t, testWizIntegration(true, false), wizSecret("tok-1"))
	tokens := r.WizTokens(t.Context())
	if len(tokens) != 1 || string(tokens[0]) != "tok-1" {
		t.Errorf("WizTokens() = %q, want [tok-1]", tokens)
	}
}

func TestWizTokensSkipsSuspendedAndOtherProviders(t *testing.T) {
	suspended := testWizIntegration(true, false)
	suspended.Spec.Suspend = true
	r := newWizReceiver(t, suspended, testIntegration(), wizSecret("tok-1"))
	if tokens := r.WizTokens(t.Context()); len(tokens) != 0 {
		t.Errorf("WizTokens() = %d candidates, want none", len(tokens))
	}
}

func TestHandleWizIssueIngests(t *testing.T) {
	r := newWizReceiver(t, testWizIntegration(true, false), wizSecret("tok-1"))
	e := webhook.Event{Type: wiz.EventIssue, Payload: []byte(wizIssueJSON), Path: WizPath}
	if err := r.handleWiz(t.Context(), e); err != nil {
		t.Fatalf("handleWiz() = %v, want nil", err)
	}
	items := listFindings(t, r.Ingest.Client)
	if len(items) != 1 {
		t.Fatalf("ingested %d findings, want 1", len(items))
	}
	f := items[0]
	if f.Spec.Source != wiz.IssuesID {
		t.Errorf("spec.source = %q, want %q", f.Spec.Source, wiz.IssuesID)
	}
	if f.Spec.CloudResource == nil || f.Spec.CloudResource.Provider != v1alpha1.CloudProviderGoogle {
		t.Fatalf("spec.cloudResource = %+v, want google", f.Spec.CloudResource)
	}
	if want := "//storage.googleapis.com/acme-artifacts"; f.Spec.CloudResource.Name != want {
		t.Errorf("cloud resource name = %q, want normalized %q", f.Spec.CloudResource.Name, want)
	}
	if f.Spec.Repository != nil {
		t.Error("spec.repository set at ingest; a wiz finding arrives repo-less")
	}
	if len(f.Spec.Advisories) == 0 || f.Spec.Advisories[0] != "wiz-control:wc-1" {
		t.Errorf("advisories = %v, want wiz-control:wc-1 first", f.Spec.Advisories)
	}
}

func TestHandleWizThreatIngests(t *testing.T) {
	r := newWizReceiver(t, testWizIntegration(false, true), wizSecret("tok-1"))
	e := webhook.Event{Type: wiz.EventThreat, Payload: []byte(wizThreatJSON), Path: WizPath}
	if err := r.handleWiz(t.Context(), e); err != nil {
		t.Fatalf("handleWiz() = %v, want nil", err)
	}
	items := listFindings(t, r.Ingest.Client)
	if len(items) != 1 {
		t.Fatalf("ingested %d findings, want 1", len(items))
	}
	if items[0].Spec.Source != wiz.DefendID {
		t.Errorf("spec.source = %q, want %q", items[0].Spec.Source, wiz.DefendID)
	}
}

// A feed the Integration does not enable drops silently: the delivery
// authenticated (the token is real), but nothing is configured to ingest it.
func TestHandleWizFeedNotEnabled(t *testing.T) {
	r := newWizReceiver(t, testWizIntegration(true, false), wizSecret("tok-1"))
	e := webhook.Event{Type: wiz.EventThreat, Payload: []byte(wizThreatJSON), Path: WizPath}
	if err := r.handleWiz(t.Context(), e); err != nil {
		t.Fatalf("handleWiz() = %v, want nil (feed off is not an error)", err)
	}
	if items := listFindings(t, r.Ingest.Client); len(items) != 0 {
		t.Errorf("ingested %d findings with the defend feed off, want none", len(items))
	}
}

func TestWizDecoder(t *testing.T) {
	event, id, err := wizDecoder(nil, []byte(wizIssueJSON))
	if err != nil {
		t.Fatalf("wizDecoder() = %v, want nil", err)
	}
	if event != wiz.EventIssue {
		t.Errorf("event = %q, want %q", event, wiz.EventIssue)
	}
	if len(id) != 16 {
		t.Errorf("delivery id = %q, want a 16-char digest", id)
	}
	if _, _, err := wizDecoder(nil, []byte("not json")); err == nil {
		t.Error("wizDecoder(undecodable) = nil error, want 400-causing error")
	}
	event, _, err = wizDecoder(nil, []byte(`{"text": "test message"}`))
	if err != nil || event != "ping" {
		t.Errorf("wizDecoder(test delivery) = %q, %v; want ping, nil", event, err)
	}
}

func TestValidateWiz(t *testing.T) {
	for _, tt := range []struct {
		name    string
		integ   *v1alpha1.Integration
		secret  *corev1.Secret
		wantErr string
	}{
		{"valid webhook-only", testWizIntegration(true, false), wizSecret("tok"), ""},
		{"no capability", testWizIntegration(false, false), wizSecret("tok"), "enables no capability"},
		{
			"missing webhook token",
			testWizIntegration(true, false),
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "wiz-creds", Namespace: "patchy"}},
			"missing key webhookToken",
		},
		{
			"api block without credentials",
			func() *v1alpha1.Integration {
				i := testWizIntegration(true, false)
				i.Spec.Wiz.API = &v1alpha1.WizAPI{Endpoint: "https://api.eu1.app.wiz.io/graphql"}
				return i
			}(),
			wizSecret("tok"),
			"missing key clientId or clientSecret",
		},
		{
			"api block with credentials",
			func() *v1alpha1.Integration {
				i := testWizIntegration(true, false)
				i.Spec.Wiz.API = &v1alpha1.WizAPI{Endpoint: "https://api.eu1.app.wiz.io/graphql"}
				return i
			}(),
			func() *corev1.Secret {
				s := wizSecret("tok")
				s.Data[wiz.KeyClientID] = []byte("id")
				s.Data[wiz.KeyClientSecret] = []byte("sec")
				return s
			}(),
			"",
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
