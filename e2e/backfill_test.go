// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/e2e/fakegithub"
)

// TestBackfill drives the manual list-alerts backfill end to end over the
// PAT-with-exact-repos path (the fake, like the e2e credentials, is
// PAT-shaped; the App installation fan-out is covered by unit fakes):
// alerts that predate webhook coverage are seeded on the fake, a
// spec.backfill request with an exact-repo filter is stamped, and the
// integration-controller lists and ingests exactly the filtered alerts,
// echoing the request on status.backfill. Both scanning capabilities are
// enabled, so code- and secret-scanning alerts come back as two finding
// families. A re-stamp is idempotent: ingestion folds the same alerts back
// into the same findings.
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

	listen := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	cl.controller(t, "integration-controller", "--listen-addr", listen)

	// No webhook was ever delivered: without a backfill these alerts are
	// invisible.
	var list v1alpha1.FindingList
	if err := cl.client.List(ctx, &list, client.InNamespace(namespace)); err != nil || len(list.Items) != 0 {
		t.Fatalf("findings before backfill = %d (err %v), want 0", len(list.Items), err)
	}

	// Second-truncated: metav1.Time serializes at second precision, and the
	// echo comparison below must survive the round trip.
	first := metav1.NewTime(time.Now().UTC().Truncate(time.Second))
	stampBackfill(t, cl, first)

	// The walk ingests the filtered code alerts into one finding family and
	// the filtered secret into another, and echoes the request.
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

	st := waitBackfillEcho(t, cl, "status.backfill to echo the request", first)
	if st.Listed != 3 || st.Ingested != 3 || st.Truncated || st.Error != "" {
		t.Errorf("status.backfill = %+v, want listed 3 ingested 3 clean", st)
	}

	// A second, newer request re-runs the walk; ingestion is idempotent, so
	// the same alerts fold back into the same finding.
	second := metav1.NewTime(first.Add(2 * time.Second))
	stampBackfill(t, cl, second)
	waitBackfillEcho(t, cl, "the second backfill to be consumed", second)
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

// stampBackfill writes a backfill request for acme/shop onto the github
// Integration, as the status page or CLI would.
func stampBackfill(t *testing.T, cl *cluster, at metav1.Time) {
	t.Helper()
	ctx := context.Background()
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var integ v1alpha1.Integration
		key := types.NamespacedName{Namespace: namespace, Name: "github"}
		if err := cl.client.Get(ctx, key, &integ); err != nil {
			return err
		}
		integ.Spec.Backfill = &v1alpha1.BackfillRequest{
			By:           "op@acme.test",
			At:           at,
			Repositories: []string{"acme/shop"},
		}
		return cl.client.Update(ctx, &integ)
	})
	if err != nil {
		t.Fatalf("stamp spec.backfill: %v", err)
	}
}

// waitBackfillEcho waits for status.backfill to report a walk at or after
// since, and returns it.
func waitBackfillEcho(t *testing.T, cl *cluster, why string, since metav1.Time) *v1alpha1.BackfillStatus {
	t.Helper()
	var integ v1alpha1.Integration
	eventually(t, why, func() bool {
		key := types.NamespacedName{Namespace: namespace, Name: "github"}
		if err := cl.client.Get(context.Background(), key, &integ); err != nil {
			return false
		}
		st := integ.Status.Backfill
		return st != nil && st.BackfilledAt != nil && !st.BackfilledAt.Time.Before(since.Time)
	})
	return integ.Status.Backfill
}
