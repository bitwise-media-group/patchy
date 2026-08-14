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
// echoing the request on status.backfill. A re-stamp is idempotent —
// ingestion folds the same alerts back into the same finding.
func TestBackfill(t *testing.T) {
	cl := startCluster(t)
	gh := fakegithub.New()
	t.Cleanup(gh.Close)
	cl.githubCredentials(t, gh.URL)
	ctx := context.Background()

	// The pre-existing estate: two alerts in acme/shop (one finding family —
	// the fake serves every alert with the same rule), one in acme/other
	// that the filter must exclude.
	gh.SeedAlert("acme", "shop", 41)
	gh.SeedAlert("acme", "shop", 42)
	gh.SeedAlert("acme", "other", 7)

	listen := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	cl.controller(t, "integration-controller", "--listen-addr", listen)

	// No webhook was ever delivered: without a backfill these alerts are
	// invisible.
	var list v1alpha1.FindingList
	if err := cl.client.List(ctx, &list, client.InNamespace(namespace)); err != nil || len(list.Items) != 0 {
		t.Fatalf("findings before backfill = %d (err %v), want 0", len(list.Items), err)
	}

	stamp := func(at metav1.Time) {
		t.Helper()
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

	// Second-truncated: metav1.Time serializes at second precision, and the
	// echo comparison below must survive the round trip.
	first := metav1.NewTime(time.Now().UTC().Truncate(time.Second))
	stamp(first)

	// The walk ingests both filtered alerts into one finding family and
	// echoes the request.
	var fnd v1alpha1.Finding
	eventually(t, "the backfilled finding to be created with both alerts", func() bool {
		var cur v1alpha1.FindingList
		if err := cl.client.List(ctx, &cur, client.InNamespace(namespace)); err != nil || len(cur.Items) != 1 {
			return false
		}
		fnd = cur.Items[0]
		return len(fnd.Spec.Alerts) == 2
	})
	if fnd.Spec.Source != "ghas" || fnd.Spec.Repository == nil ||
		fnd.Spec.Repository.Name != "acme/shop" {
		t.Errorf("backfilled finding = source %q repository %+v, want ghas acme/shop",
			fnd.Spec.Source, fnd.Spec.Repository)
	}

	var integ v1alpha1.Integration
	eventually(t, "status.backfill to echo the request", func() bool {
		key := types.NamespacedName{Namespace: namespace, Name: "github"}
		if err := cl.client.Get(ctx, key, &integ); err != nil {
			return false
		}
		st := integ.Status.Backfill
		return st != nil && st.BackfilledAt != nil && !st.BackfilledAt.Time.Before(first.Time)
	})
	st := integ.Status.Backfill
	if st.Listed != 2 || st.Ingested != 2 || st.Truncated || st.Error != "" {
		t.Errorf("status.backfill = %+v, want listed 2 ingested 2 clean", st)
	}

	// A second, newer request re-runs the walk; ingestion is idempotent, so
	// the same alerts fold back into the same finding.
	second := metav1.NewTime(first.Add(2 * time.Second))
	stamp(second)
	eventually(t, "the second backfill to be consumed", func() bool {
		key := types.NamespacedName{Namespace: namespace, Name: "github"}
		if err := cl.client.Get(ctx, key, &integ); err != nil {
			return false
		}
		st := integ.Status.Backfill
		return st != nil && st.BackfilledAt != nil && !st.BackfilledAt.Time.Before(second.Time)
	})
	if err := cl.client.List(ctx, &list, client.InNamespace(namespace)); err != nil || len(list.Items) != 1 {
		t.Fatalf("findings after re-backfill = %d (err %v), want still 1", len(list.Items), err)
	}
	if got := len(list.Items[0].Spec.Alerts); got != 2 {
		t.Errorf("alerts after re-backfill = %d, want still 2 (no duplicates)", got)
	}
}
