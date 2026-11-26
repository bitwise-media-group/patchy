// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/e2e/fakegithub"
)

// TestBackfill drives the manual list-alerts backfill end to end over the
// PAT-with-exact-repos path (the App installation fan-out is covered by
// TestGitHubMaintainer): alerts that predate webhook coverage are seeded on
// the fake, a BackfillRequest with an exact-repo filter is created, and the
// integration-controller lists and ingests exactly the filtered alerts,
// recording the walk on the request's status. Both scanning capabilities
// are enabled, so code- and secret-scanning alerts come back as two finding
// families. A second request is idempotent: ingestion folds the same
// alerts back into the same findings. Finished requests expire on the TTL.
func TestBackfill(t *testing.T) {
	cl := startCluster(t)
	gh := fakegithub.New()
	t.Cleanup(gh.Close)
	cl.githubCredentials(t, gh.URL)
	ctx := context.Background()

	// The pre-existing estate: two alerts in acme/shop (one finding family,
	// since the fake serves every alert with the same rule), one in
	// acme/other that the filter must exclude.
	gh.SeedAlert("acme", "shop", 41)
	gh.SeedAlert("acme", "shop", 42)
	gh.SeedAlert("acme", "other", 7)
	// And one leaked secret in each repository, again filtered to acme/shop.
	gh.SeedSecretAlert("acme", "shop", 5)
	gh.SeedSecretAlert("acme", "other", 9)

	// A short TTL, so the expiry of finished requests is observable. It
	// still leaves ample time to read each request's status.
	const ttl = 8 * time.Second
	listen := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	cl.controller(t, "integration-controller", "--listen-addr", listen,
		"--backfill-request-ttl", ttl.String())

	// No webhook was ever delivered: without a backfill these alerts are
	// invisible.
	var list v1alpha1.FindingList
	if err := cl.client.List(ctx, &list, client.InNamespace(namespace)); err != nil || len(list.Items) != 0 {
		t.Fatalf("findings before backfill = %d (err %v), want 0", len(list.Items), err)
	}

	// A request the Integration cannot run yet (its credential not yet
	// validated) is held for minutes, so file it once the Integration is
	// Ready, as an operator would.
	waitIntegrationReady(t, cl, "github")
	first := createBackfillRequest(t, cl, "github", "op@acme.test", "acme/shop")

	backfillCheckFirst(t, cl, first)
	second := createBackfillRequest(t, cl, "github", "op@acme.test", "acme/shop")
	backfillCheckRepeat(t, cl, second)

	// A request naming an Integration that does not exist fails for good
	// rather than retrying.
	orphan := createBackfillRequest(t, cl, "no-such-integration", "op@acme.test", "acme/shop")
	if st := waitBackfillPhase(t, cl, orphan, v1alpha1.BackfillFailed); st.Error == "" {
		t.Errorf("orphan request status = %+v, want an error", st)
	}

	// Finished requests are deleted once the TTL has passed.
	for _, name := range []string{first, second, orphan} {
		eventually(t, "backfill request "+name+" to expire", func() bool {
			var bfr v1alpha1.BackfillRequest
			err := cl.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &bfr)
			return kerrors.IsNotFound(err)
		})
	}
}

// backfillCheckFirst checks the first walk: the filtered code alerts in one
// finding family, the filtered secret in another, and the walk's counts on
// the request.
func backfillCheckFirst(t *testing.T, cl *cluster, first string) {
	t.Helper()
	ctx := context.Background()
	// The walk ingests the filtered code alerts into one finding family and
	// the filtered secret into another.
	bySource := map[string]v1alpha1.Finding{}
	eventually(t, "the backfilled findings to be created", func() bool {
		var cur v1alpha1.FindingList
		if err := cl.client.List(ctx, &cur, client.InNamespace(namespace)); err != nil || len(cur.Items) != 2 {
			return false
		}
		clear(bySource)
		for _, f := range cur.Items {
			bySource[f.Spec.Source] = f
		}
		return len(bySource["ghas"].Spec.Alerts) == 2 && len(bySource["ghas-secrets"].Spec.Alerts) == 1
	})
	for src, fnd := range bySource {
		if fnd.Spec.Repository == nil || fnd.Spec.Repository.Name != "acme/shop" {
			t.Errorf("backfilled %s finding repository = %+v, want acme/shop", src, fnd.Spec.Repository)
		}
	}
	if sec := bySource["ghas-secrets"]; sec.Spec.Category != v1alpha1.FindingCategorySecret ||
		sec.Spec.Alerts[0].ID != "5" {
		t.Errorf("secret finding = category %q alerts %+v, want secret alert 5", sec.Spec.Category, sec.Spec.Alerts)
	}

	st := waitBackfillPhase(t, cl, first, v1alpha1.BackfillComplete)
	if st.Listed != 3 || st.Ingested != 3 || st.Truncated || st.Error != "" || st.Attempts != 1 ||
		st.CompletedAt == nil {
		t.Errorf("first request status = %+v, want complete after 1 attempt, listed 3 ingested 3 clean", st)
	}
}

// backfillCheckRepeat checks a second walk of the same repository folds the
// same alerts back into the same findings.
func backfillCheckRepeat(t *testing.T, cl *cluster, second string) {
	t.Helper()
	ctx := context.Background()
	// A second request re-runs the walk; ingestion is idempotent, so the
	// same alerts fold back into the same findings.
	st := waitBackfillPhase(t, cl, second, v1alpha1.BackfillComplete)
	if st.Listed != 3 || st.Ingested != 3 {
		t.Errorf("second request status = %+v, want listed 3 ingested 3", st)
	}
	var list v1alpha1.FindingList
	if err := cl.client.List(ctx, &list, client.InNamespace(namespace)); err != nil || len(list.Items) != 2 {
		t.Fatalf("findings after re-backfill = %d (err %v), want still 2", len(list.Items), err)
	}
	alerts := 0
	for _, f := range list.Items {
		alerts += len(f.Spec.Alerts)
	}
	if alerts != 3 {
		t.Errorf("alerts after re-backfill = %d, want still 3 (no duplicates)", alerts)
	}
}

// createBackfillRequest files a granted backfill of repos through the named
// Integration, as the CLI would, and returns its name.
func createBackfillRequest(t *testing.T, cl *cluster, integration, by string, repos ...string) string {
	t.Helper()
	bfr := &v1alpha1.BackfillRequest{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, GenerateName: "backfill-"},
		Spec: v1alpha1.BackfillRequestSpec{
			IntegrationRef: v1alpha1.LocalObjectReference{Name: integration},
			RequestedBy:    by,
			Repositories:   repos,
		},
	}
	if err := cl.client.Create(context.Background(), bfr); err != nil {
		t.Fatalf("create backfill request: %v", err)
	}
	return bfr.Name
}

// waitIntegrationReady waits for the named Integration's Ready condition.
func waitIntegrationReady(t *testing.T, cl *cluster, name string) {
	t.Helper()
	eventually(t, "integration "+name+" to be ready", func() bool {
		var integ v1alpha1.Integration
		key := types.NamespacedName{Namespace: namespace, Name: name}
		if err := cl.client.Get(context.Background(), key, &integ); err != nil {
			return false
		}
		return meta.IsStatusConditionTrue(integ.Status.Conditions, v1alpha1.ConditionReady)
	})
}

// getBackfillRequest reads one BackfillRequest, failing the test on error.
func getBackfillRequest(t *testing.T, cl *cluster, name string) *v1alpha1.BackfillRequest {
	t.Helper()
	var bfr v1alpha1.BackfillRequest
	key := types.NamespacedName{Namespace: namespace, Name: name}
	if err := cl.client.Get(context.Background(), key, &bfr); err != nil {
		t.Fatalf("get backfill request %s: %v", name, err)
	}
	return &bfr
}

// waitBackfillPhase waits for the named request to reach phase and returns
// its status.
func waitBackfillPhase(
	t *testing.T, cl *cluster, name string, phase v1alpha1.BackfillRequestPhase,
) v1alpha1.BackfillRequestStatus {
	t.Helper()
	var bfr v1alpha1.BackfillRequest
	eventually(t, fmt.Sprintf("backfill request %s to reach %s", name, phase), func() bool {
		key := types.NamespacedName{Namespace: namespace, Name: name}
		if err := cl.client.Get(context.Background(), key, &bfr); err != nil {
			return false
		}
		return bfr.Status.Phase == phase
	})
	return bfr.Status
}
