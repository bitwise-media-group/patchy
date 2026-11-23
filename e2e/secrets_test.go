// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/e2e/fakegithub"
)

// fixtureSecretValue is the plaintext the secret_scanning_alert fixture
// carries, as a real delivery would. It is obviously fake and not
// token-shaped, so push protection never trips on it.
const fixtureSecretValue = "patchy-e2e-fixture-value-not-a-credential"

// TestSecretScanning drives a secret-scanning delivery end to end through
// the integration-controller: the Finding carries the secret category, the
// derived severity and the located path, and the plaintext the delivery
// carried reaches neither the Finding nor the tracking issue. The fake
// rejects any secret-scanning read without hide_secret=true, so ingestion
// succeeding proves patchy asked for the value to be hidden. A confident
// ignore then resolves the alert on GitHub with the agent's reason.
func TestSecretScanning(t *testing.T) {
	cl := startCluster(t)
	gh := fakegithub.New()
	t.Cleanup(gh.Close)
	cl.githubCredentials(t, gh.URL)
	ctx := context.Background()

	listen := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	cl.controller(t, "integration-controller", "--listen-addr", listen)
	deliver(t, "http://"+listen+"/github/webhooks", "secret_scanning_alert",
		fixture(t, "secret_scanning_alert.created.json"))

	var fnd v1alpha1.Finding
	eventually(t, "the secret finding to be created", func() bool {
		var list v1alpha1.FindingList
		if err := cl.client.List(ctx, &list, client.InNamespace(namespace)); err != nil || len(list.Items) != 1 {
			return false
		}
		fnd = list.Items[0]
		return true
	})
	if fnd.Spec.Source != "ghas-secrets" || fnd.Spec.Category != v1alpha1.FindingCategorySecret ||
		fnd.Spec.Severity != v1alpha1.LevelHigh {
		t.Errorf("finding = source %q category %q severity %q, want ghas-secrets/secret/high",
			fnd.Spec.Source, fnd.Spec.Category, fnd.Spec.Severity)
	}
	if want := "secret:" + fakegithub.SecretAlertType; len(fnd.Spec.Advisories) == 0 || fnd.Spec.Advisories[0] != want {
		t.Errorf("advisories = %v, want %s", fnd.Spec.Advisories, want)
	}
	if len(fnd.Spec.Alerts) != 1 || len(fnd.Spec.Alerts[0].Locations) != 1 ||
		fnd.Spec.Alerts[0].Locations[0].Path != "config/app.yaml" ||
		fnd.Spec.Alerts[0].Locations[0].StartColumn != 10 {
		t.Errorf("alerts = %+v, want one located at config/app.yaml:4 col 10", fnd.Spec.Alerts)
	}
	raw, err := json.Marshal(fnd)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), fixtureSecretValue) {
		t.Error("the Finding carries the secret value")
	}

	eventually(t, "the tracking issue to be projected", func() bool { return len(gh.Issues()) == 1 })
	issue := gh.Issues()[0]
	if strings.Contains(issue.Title+issue.Body, fixtureSecretValue) {
		t.Error("the tracking issue carries the secret value")
	}
	for _, want := range []string{"Rotate or revoke", "config/app.yaml:4"} {
		if !strings.Contains(issue.Body, want) {
			t.Errorf("issue body missing %q:\n%s", want, issue.Body)
		}
	}

	// A confident ignore (the investigation-controller's routing is unit
	// tested) lands as Dismissed with the agent's reason; the projection
	// resolves the alert on GitHub with it.
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var cur v1alpha1.Finding
		if err := cl.client.Get(ctx, client.ObjectKeyFromObject(&fnd), &cur); err != nil {
			return err
		}
		cur.Status.Phase = v1alpha1.PhaseDismissed
		cur.Status.Investigation = &v1alpha1.InvestigationSummary{
			Name: "inv", Attempt: 1, Recommendation: v1alpha1.RecommendationIgnore,
			DismissReason: v1alpha1.DismissUsedInTests, Confidence: "0.95",
		}
		return cl.client.Status().Update(ctx, &cur)
	})
	if err != nil {
		t.Fatalf("dismiss the finding: %v", err)
	}
	eventually(t, "the secret alert to be resolved", func() bool {
		return gh.ResolvedSecrets()[5] == "used_in_tests"
	})
	if got := gh.Dismissed(); len(got) != 0 {
		t.Errorf("code-scanning dismissals = %v, want none for a secret", got)
	}
}
