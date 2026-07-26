// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package integration

import (
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/ghclient"
)

// projectableCloud is a repo-less cloud finding ready for projection.
func projectableCloud(phase v1alpha1.Phase) *v1alpha1.Finding {
	fnd := projectable(phase)
	fnd.Spec.Source = "gcp-scc"
	fnd.Spec.Repository = nil
	fnd.Spec.CloudResource = &v1alpha1.FindingCloudResource{
		Provider: v1alpha1.CloudProviderGoogle,
		Name:     "//storage.googleapis.com/projects/acme-prod/buckets/artifacts",
	}
	fnd.Spec.Alerts = []v1alpha1.Alert{{
		ID: "organizations/1/sources/2/findings/abc", Source: "gcp-scc",
	}}
	return fnd
}

// withFallback returns the tracking Integration configured with a catch-all
// repository for findings that have none of their own.
func withFallback() *v1alpha1.Integration {
	integ := testIntegration()
	integ.Spec.GitHub.Issues.FallbackRepository = "acme/security"
	return integ
}

// A cloud finding's repository is resolved by the enhancer chain at Opened.
// Opening its issue before then would file it on the fallback repository
// permanently — an issue cannot be moved once created.
func TestProjectionDefersUnresolvedCloudFinding(t *testing.T) {
	tracker := newFakeTracker()
	r, c := newProjector(t, tracker,
		withFallback(), projectableCloud(v1alpha1.PhaseOpened))

	reconcileFinding(t, r)

	if len(tracker.issues) != 0 {
		t.Errorf("issues = %v, want none while the repository is still being resolved", tracker.issues)
	}
	if get(t, c, "finding-aa-1").Status.Tracking != nil {
		t.Error("status.tracking is set; the finding was filed before its repository was known")
	}
}

// Deferring the projection must not defer the accumulation window: the
// investigation gate blocks on that condition, and closeAccumulation is its
// only writer. Skipping the whole reconcile would strand the finding forever.
func TestDeferredProjectionStillClosesAccumulation(t *testing.T) {
	fnd := projectableCloud(v1alpha1.PhaseOpened)
	elapsed := metav1.NewTime(testClock.Add(-time.Hour))
	fnd.Status.AccumulateUntil = &elapsed

	tracker := newFakeTracker()
	r, c := newProjector(t, tracker, withFallback(), fnd)

	reconcileFinding(t, r)

	got := get(t, c, "finding-aa-1")
	if !meta.IsStatusConditionTrue(got.Status.Conditions, v1alpha1.ConditionAccumulationComplete) {
		t.Error("AccumulationComplete is not set; the finding can never pass the investigation gate")
	}
}

// Once enhancement has run, a finding still without a repository is filed on
// the fallback so the humans who must triage it can see it.
func TestRepoLessFindingProjectsToFallback(t *testing.T) {
	tracker := newFakeTracker()
	r, c := newProjector(t, tracker,
		withFallback(), projectableCloud(v1alpha1.PhaseEnhanced))

	reconcileFinding(t, r)

	if len(tracker.issues) != 1 {
		t.Fatalf("issues = %d, want the finding filed on the fallback repository", len(tracker.issues))
	}
	if got := tracker.issues[7].Repo; got != (ghclient.Repo{Owner: "acme", Name: "security"}) {
		t.Errorf("issue repo = %+v, want acme/security", got)
	}
	if get(t, c, "finding-aa-1").Status.Tracking == nil {
		t.Error("status.tracking is nil after a fallback projection")
	}
}

// Without a fallback the finding stays kubectl- and status-page-only. That is
// a deliberate configuration choice, not an error to retry.
func TestRepoLessFindingWithoutFallbackIsNotProjected(t *testing.T) {
	tracker := newFakeTracker()
	r, _ := newProjector(t, tracker, testIntegration(), projectableCloud(v1alpha1.PhaseEnhanced))

	reconcileFinding(t, r)

	if len(tracker.issues) != 0 {
		t.Errorf("issues = %v, want none without a configured fallback", tracker.issues)
	}
}

// A finding with its own repository ignores the fallback entirely.
func TestFindingWithRepositoryIgnoresFallback(t *testing.T) {
	tracker := newFakeTracker()
	r, _ := newProjector(t, tracker,
		withFallback(), projectable(v1alpha1.PhaseEnhanced))

	reconcileFinding(t, r)

	if got := tracker.issues[7].Repo; got != (ghclient.Repo{Owner: "acme", Name: "orders"}) {
		t.Errorf("issue repo = %+v, want the finding's own acme/orders", got)
	}
}

// The write-back is owed whether or not the finding has a tracking issue: an
// alert dismissed here must be dismissed in the tool it came from too.
func TestDismissedRepoLessFindingStillResolvesItsSource(t *testing.T) {
	fnd := projectableCloud(v1alpha1.PhaseDismissed)
	// Give it a GHAS alert as well, to prove routing is per-alert.
	fnd.Spec.Alerts = append(fnd.Spec.Alerts, v1alpha1.Alert{ID: "42", Source: "ghas"})

	tracker := newFakeTracker()
	r, c := newProjector(t, tracker, testIntegration(), fnd)

	reconcileFinding(t, r)

	// The GHAS alert is dismissed even though the finding has no repository
	// of its own and no tracking issue at all.
	if len(tracker.dismissed) != 0 {
		t.Errorf("dismissed = %v; a repo-less finding has no GitHub repo to dismiss against",
			tracker.dismissed)
	}
	// The pass is still recorded, so it is not retried on every reconcile.
	if got := get(t, c, "finding-aa-1").Annotations[AnnotationResolvedSource]; got != "Dismissed" {
		t.Errorf("resolved-source annotation = %q, want Dismissed", got)
	}
}

// Alerts are routed by the source recorded on them, not by the shape of their
// id: an SCC finding name must never be offered to GitHub's dismissal API.
func TestAlertsRouteBySourceNotIDShape(t *testing.T) {
	fnd := projectable(v1alpha1.PhaseDismissed)
	fnd.Status.Tracking = &v1alpha1.TrackingStatus{Integration: "gh", IssueNumber: 7, State: "open"}
	tracker := newFakeTracker()
	tracker.issues[7] = &ghclient.Issue{Number: 7, State: "open"}
	fnd.Spec.Alerts = []v1alpha1.Alert{
		{ID: "42", Source: "ghas"},
		{ID: "organizations/1/sources/2/findings/abc", Source: "gcp-scc"},
	}
	r, _ := newProjector(t, tracker, testIntegration(), fnd)

	reconcileFinding(t, r)

	if len(tracker.dismissed) != 1 || tracker.dismissed[0] != 42 {
		t.Errorf("dismissed = %v, want only the GHAS alert 42", tracker.dismissed)
	}
}

func TestAlertsBySource(t *testing.T) {
	fnd := &v1alpha1.Finding{Spec: v1alpha1.FindingSpec{
		Source: "ghas",
		Alerts: []v1alpha1.Alert{
			{ID: "1", Source: "ghas"},
			{ID: "2", Source: "gcp-scc"},
			// Written before Alert.Source existed: spec.source is the answer,
			// and was accurate while accumulation was the only way alerts
			// arrived.
			{ID: "3"},
		},
	}}

	got := fnd.AlertsBySource()
	if len(got["ghas"]) != 2 {
		t.Errorf("ghas alerts = %+v, want the tagged one and the legacy one", got["ghas"])
	}
	if len(got["gcp-scc"]) != 1 {
		t.Errorf("gcp-scc alerts = %+v, want one", got["gcp-scc"])
	}
}

// An alert whose source cannot be determined is dropped: dismissing it
// against the wrong tool is worse than not dismissing it.
func TestAlertsBySourceDropsUnattributable(t *testing.T) {
	fnd := &v1alpha1.Finding{Spec: v1alpha1.FindingSpec{
		Alerts: []v1alpha1.Alert{{ID: "1"}},
	}}
	if got := fnd.AlertsBySource(); len(got) != 0 {
		t.Errorf("AlertsBySource() = %+v, want empty", got)
	}
}
