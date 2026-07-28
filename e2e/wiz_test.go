// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package e2e

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
)

// wizWebhookToken is the shared bearer token the wiz Integration's automation
// actions present. The fixtures under fixtures/webhooks/wiz.* are hand-built
// to the automation-rule body templates documented in docs/integrations/wiz.md
// — that document is the payload contract the decoder is tested against.
const wizWebhookToken = "e2e-wiz-webhook-token"

// TestWizWebhookPipeline drives Wiz Issues and Wiz Defend deliveries through
// the real receiver, over the real bearer-token authentication path, and
// asserts the Findings they become.
//
// No google-cloud Integration is seeded, so the asset-inventory enhancer
// stands aside and the findings travel the repo-less route: ingested,
// enhanced (with RepositoryUnresolved), ready for handoff — the same proof
// the SCC test makes. The Integration-driven enhancer configuration itself is
// covered by unit tests; exercising it here would need a fake Cloud Asset
// Inventory, which does not exist yet.
func TestWizWebhookPipeline(t *testing.T) {
	cl := startCluster(t)
	ctx := t.Context()

	seedWizIntegration(t, cl)

	listen := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	cl.controller(t, "integration-controller", "--listen-addr", listen)
	cl.controller(t, "context-controller")
	url := "http://" + listen + "/wiz/webhooks"

	issue := fixture(t, "wiz.issue.created.json")

	// Authentication first: the route must fail closed before anything is
	// ingested, or the assertions below prove nothing about who may deliver.
	t.Run("rejects deliveries it cannot authenticate", func(t *testing.T) {
		for _, tt := range []struct{ name, token string }{
			{"no token at all", ""},
			{"the wrong token", "not-the-token"},
			{"the token as a prefix", wizWebhookToken[:8]},
		} {
			t.Run(tt.name, func(t *testing.T) {
				if got := postWiz(t, url, tt.token, issue); got != http.StatusUnauthorized {
					t.Errorf("status = %d, want 401", got)
				}
			})
		}
	})

	// Wiz's connectivity test posts a body that is neither an issue nor a
	// threat; it must be answered 204 rather than rejected, or the operator
	// cannot save the automation action.
	t.Run("answers the test delivery with 204", func(t *testing.T) {
		if got := postWiz(t, url, wizWebhookToken, []byte(`{"text": "test message"}`)); got != http.StatusNoContent {
			t.Errorf("test delivery = %d, want 204", got)
		}
	})

	// Now the real thing.
	if got := postWiz(t, url, wizWebhookToken, issue); got != http.StatusAccepted {
		t.Fatalf("authenticated delivery = %d, want 202", got)
	}

	var fnd v1alpha1.Finding
	eventually(t, "the issue to become a finding", func() bool {
		var list v1alpha1.FindingList
		if err := cl.client.List(ctx, &list, client.InNamespace(namespace)); err != nil {
			return false
		}
		for i := range list.Items {
			if list.Items[i].Spec.Source == "wiz-issues" {
				fnd = list.Items[i]
				return true
			}
		}
		return false
	})

	// The GCP providerId arrives as an API self-link and must be normalized
	// to the Cloud Asset Inventory name form at ingest, or the enhancer could
	// never resolve ownership labels for it.
	if fnd.Spec.CloudResource == nil {
		t.Fatal("spec.cloudResource is nil; the finding cannot be attributed to anything")
	}
	if got, want := fnd.Spec.CloudResource.Name, "//storage.googleapis.com/acme-artifacts"; got != want {
		t.Errorf("cloudResource.name = %q, want the normalized %q", got, want)
	}
	if got := fnd.Spec.CloudResource.Provider; got != v1alpha1.CloudProviderGoogle {
		t.Errorf("cloudResource.provider = %q, want google", got)
	}
	if fnd.Spec.Repository != nil {
		t.Errorf("spec.repository = %+v, want nil", fnd.Spec.Repository)
	}
	if len(fnd.Spec.Alerts) != 1 || fnd.Spec.Alerts[0].ID != "e2e00000-0000-0000-0000-00000000w1z1" {
		t.Errorf("alerts = %+v, want the wiz issue id", fnd.Spec.Alerts)
	}
	if got := fnd.Spec.Advisories[0]; got != "wiz-control:wc-e2e-public-bucket" {
		t.Errorf("advisories[0] = %q, want the control key", got)
	}
	if fnd.Spec.Severity != v1alpha1.LevelHigh {
		t.Errorf("severity = %q, want high", fnd.Spec.Severity)
	}
	for _, want := range []string{"public read access", "allUsers member"} {
		if !strings.Contains(fnd.Spec.Description, want) {
			t.Errorf("description missing %q", want)
		}
	}

	// A Wiz re-notification is byte-identical only when nothing changed; the
	// delivery dedup window absorbs it either way, and ingest folds updates.
	if got := postWiz(t, url, wizWebhookToken, issue); got != http.StatusAccepted {
		t.Fatalf("redelivery = %d, want 202", got)
	}
	consistently(t, "the redelivery not to open a second finding", func() bool {
		return len(findingsBySource(t, cl, "wiz-issues")) == 1
	})

	// An issue on an unsupported-for-enhancement platform still ingests: AWS
	// resources get provider aws and travel the same repo-less route.
	if got := postWiz(t, url, wizWebhookToken, fixture(t, "wiz.issue.aws.json")); got != http.StatusAccepted {
		t.Fatalf("aws issue delivery = %d, want 202", got)
	}
	eventually(t, "the aws issue to become a finding", func() bool {
		for _, f := range findingsBySource(t, cl, "wiz-issues") {
			if f.Spec.CloudResource != nil && f.Spec.CloudResource.Provider == v1alpha1.CloudProviderAWS {
				return f.Spec.CloudResource.Name == "arn:aws:s3:::acme-legacy-assets"
			}
		}
		return false
	})

	// A Defend threat names two resources; each becomes its own finding, so
	// each accumulates against the resource it concerns.
	if got := postWiz(t, url, wizWebhookToken, fixture(t, "wiz.threat.detected.json")); got != http.StatusAccepted {
		t.Fatalf("threat delivery = %d, want 202", got)
	}
	eventually(t, "the threat to become one finding per resource", func() bool {
		return len(findingsBySource(t, cl, "wiz-defend")) == 2
	})

	// The enhancer chain advances the repo-less findings rather than
	// stalling them; with no repository they are headed for handoff.
	eventually(t, "the wiz findings to be enhanced", func() bool {
		for _, f := range findingsBySource(t, cl, "wiz-issues") {
			if f.Status.Phase != v1alpha1.PhaseEnhanced {
				return false
			}
		}
		return true
	})
}

// findingsBySource lists the namespace's findings from one source.
func findingsBySource(t *testing.T, cl *cluster, source string) []v1alpha1.Finding {
	t.Helper()
	var list v1alpha1.FindingList
	if err := cl.client.List(t.Context(), &list, client.InNamespace(namespace)); err != nil {
		return nil
	}
	var out []v1alpha1.Finding
	for i := range list.Items {
		if list.Items[i].Spec.Source == source {
			out = append(out, list.Items[i])
		}
	}
	return out
}

// seedWizIntegration creates the wiz Integration and the credential Secret
// carrying its webhook token.
func seedWizIntegration(t *testing.T, cl *cluster) {
	t.Helper()
	ctx := t.Context()
	if err := cl.client.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "patchy-wiz", Namespace: namespace},
		StringData: map[string]string{"webhookToken": wizWebhookToken},
	}); err != nil {
		t.Fatal(err)
	}
	if err := cl.client.Create(ctx, &v1alpha1.Integration{
		ObjectMeta: metav1.ObjectMeta{Name: "wiz", Namespace: namespace},
		Spec: v1alpha1.IntegrationSpec{
			Provider:  v1alpha1.IntegrationProviderWiz,
			SecretRef: &v1alpha1.LocalSecretReference{Name: "patchy-wiz"},
			Wiz: &v1alpha1.WizIntegration{
				Issues: &v1alpha1.WizIssues{Enabled: true, MinSeverity: v1alpha1.LevelLow},
				Defend: &v1alpha1.WizDefend{Enabled: true, MinSeverity: v1alpha1.LevelLow},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
}

// postWiz delivers a Wiz automation-action body, presenting token as the
// bearer credential; an empty token sends no Authorization header at all.
func postWiz(t *testing.T, url, token string, payload []byte) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("deliver wiz payload: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}
