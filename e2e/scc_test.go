// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package e2e

import (
	"bytes"
	"fmt"
	"net/http"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/e2e/fakeoidc"
)

// The identity and audience the Integration is configured to trust. Anything
// else presenting a validly-signed Google token must still be rejected —
// signing proves the issuer, not the caller.
const (
	sccServiceAccount = "patchy-scc-push@x-patchy-app.iam.gserviceaccount.com"
	sccAudience       = "https://patchy.e2e.invalid/google-cloud/webhooks"
)

// TestSCCNotificationPipeline drives a recorded Security Command Center
// notification through the real receiver, over the real OIDC authentication
// path, and asserts the Finding it becomes.
//
// A cloud finding has no repository, so this is also the end-to-end proof of
// the repo-less route: the finding ingests, is enhanced, and is handed off
// rather than stalling somewhere without one.
func TestSCCNotificationPipeline(t *testing.T) {
	cl := startCluster(t)
	ctx := t.Context()

	oidc, err := fakeoidc.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(oidc.Close)

	seedSCCIntegration(t, cl)

	listen := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	cl.controller(t, "integration-controller",
		"--listen-addr", listen, "--google-oidc-issuer", oidc.URL)
	cl.controller(t, "context-controller")
	url := "http://" + listen + "/google-cloud/webhooks"

	payload := fixture(t, "scc.notification.json")

	// Authentication first: the route must fail closed before anything is
	// ingested, or the assertions below prove nothing about who may deliver.
	t.Run("rejects deliveries it cannot authenticate", func(t *testing.T) {
		expired, err := oidc.ExpiredToken(sccServiceAccount, sccAudience)
		if err != nil {
			t.Fatal(err)
		}
		unverified, err := oidc.UnverifiedToken(sccServiceAccount, sccAudience)
		if err != nil {
			t.Fatal(err)
		}
		wrongAccount, err := oidc.Token("someone-else@evil.example", sccAudience)
		if err != nil {
			t.Fatal(err)
		}
		wrongAudience, err := oidc.Token(sccServiceAccount, "https://elsewhere.invalid")
		if err != nil {
			t.Fatal(err)
		}

		for _, tt := range []struct{ name, token string }{
			{"no token at all", ""},
			{"a token that is not a JWT", "not-a-token"},
			{"an expired token", expired},
			{"a token whose email claim is unverified", unverified},
			// Anyone with a Google Cloud account can obtain a validly-signed
			// token; the identity and audience are what bind one to us.
			{"a token for another service account", wrongAccount},
			{"a token for another audience", wrongAudience},
		} {
			t.Run(tt.name, func(t *testing.T) {
				if got := postSCC(t, url, tt.token, payload); got != http.StatusUnauthorized {
					t.Errorf("status = %d, want 401", got)
				}
			})
		}
	})

	// Now the real thing.
	token, err := oidc.Token(sccServiceAccount, sccAudience)
	if err != nil {
		t.Fatal(err)
	}
	if got := postSCC(t, url, token, payload); got != http.StatusAccepted {
		t.Fatalf("authenticated delivery = %d, want 202", got)
	}

	var fnd v1alpha1.Finding
	eventually(t, "the notification to become a finding", func() bool {
		var list v1alpha1.FindingList
		if err := cl.client.List(ctx, &list, client.InNamespace(namespace)); err != nil {
			return false
		}
		for i := range list.Items {
			if list.Items[i].Spec.Source == "gcp-scc" {
				fnd = list.Items[i]
				return true
			}
		}
		return false
	})

	// The cloud resource is what an enhancer would resolve a repository from,
	// and what the status page shows in place of one.
	if fnd.Spec.CloudResource == nil {
		t.Fatal("spec.cloudResource is nil; the finding cannot be attributed to anything")
	}
	const wantResource = "//storage.googleapis.com/projects/acme-prod/buckets/acme-artifacts"
	if got := fnd.Spec.CloudResource.Name; got != wantResource {
		t.Errorf("cloudResource.name = %q, want %q", got, wantResource)
	}
	if got := fnd.Spec.CloudResource.Provider; got != v1alpha1.CloudProviderGoogle {
		t.Errorf("cloudResource.provider = %q, want google", got)
	}

	// No enhancer resolved a repository here, so the finding stays repo-less
	// all the way through — the case that used to have no coverage at all.
	if fnd.Spec.Repository != nil {
		t.Errorf("spec.repository = %+v, want nil", fnd.Spec.Repository)
	}

	// The opaque SCC finding name survives as the alert id; coercing it
	// through an int would have made it 0.
	if len(fnd.Spec.Alerts) != 1 {
		t.Fatalf("alerts = %d, want 1", len(fnd.Spec.Alerts))
	}
	alert := fnd.Spec.Alerts[0]
	if alert.ID != "organizations/123456789012/sources/5566778899/findings/e2e0000000000000000000000000001" {
		t.Errorf("alert id = %q, want the SCC finding name", alert.ID)
	}
	// Provenance travels with the alert, so the verdict write-back routes it
	// without guessing from the id's shape.
	if alert.Source != "gcp-scc" {
		t.Errorf("alert source = %q, want gcp-scc", alert.Source)
	}

	if got := fnd.Spec.Advisories[0]; got != "category:PUBLIC_BUCKET_ACL" {
		t.Errorf("advisories[0] = %q, want the category key", got)
	}
	if fnd.Spec.Severity != v1alpha1.LevelHigh {
		t.Errorf("severity = %q, want high", fnd.Spec.Severity)
	}
	// The detector's own prose reaches the finding, for the human and the agent.
	for _, want := range []string{"world-readable", "Remove the allUsers member"} {
		if !bytes.Contains([]byte(fnd.Spec.Description), []byte(want)) {
			t.Errorf("description missing %q", want)
		}
	}

	// SCC re-notifies on every update to a finding; those fold into the one
	// Finding rather than piling up.
	if got := postSCC(t, url, token, payload); got != http.StatusAccepted {
		t.Fatalf("redelivery = %d, want 202", got)
	}
	consistently(t, "the redelivery not to open a second finding", func() bool {
		var list v1alpha1.FindingList
		if err := cl.client.List(ctx, &list, client.InNamespace(namespace)); err != nil {
			return false
		}
		n := 0
		for i := range list.Items {
			if list.Items[i].Spec.Source == "gcp-scc" {
				n++
			}
		}
		return n == 1
	})

	// The enhancer chain advances it, and with no repository the gate hands it
	// to a human rather than stalling it.
	eventually(t, "the repo-less finding to be enhanced", func() bool {
		var cur v1alpha1.Finding
		if err := cl.client.Get(ctx, client.ObjectKeyFromObject(&fnd), &cur); err != nil {
			return false
		}
		return cur.Status.Phase == v1alpha1.PhaseEnhanced
	})
}

// seedSCCIntegration creates the google-cloud Integration the receiver
// authenticates against. It carries no secretRef: the provider holds no
// credential.
func seedSCCIntegration(t *testing.T, cl *cluster) {
	t.Helper()
	if err := cl.client.Create(t.Context(), &v1alpha1.Integration{
		ObjectMeta: metav1.ObjectMeta{Name: "google-cloud", Namespace: namespace},
		Spec: v1alpha1.IntegrationSpec{
			Provider: v1alpha1.IntegrationProviderGoogleCloud,
			GoogleCloud: &v1alpha1.GoogleCloudIntegration{
				SecurityCommandCenter: &v1alpha1.GoogleCloudSCC{
					Enabled:        true,
					Audience:       sccAudience,
					ServiceAccount: sccServiceAccount,
					Organization:   "123456789012",
					MinSeverity:    v1alpha1.LevelLow,
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
}

// consistently asserts a condition holds for a short settling window. Unlike
// eventually it is checking that nothing happens, so it has to wait out the
// work rather than stop at the first success.
func consistently(t *testing.T, why string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !cond() {
			t.Fatalf("expected %s", why)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// postSCC delivers a Pub/Sub push envelope, presenting token as the bearer
// credential; an empty token sends no Authorization header at all.
func postSCC(t *testing.T, url, token string, payload []byte) int {
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
		t.Fatalf("deliver scc notification: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}
