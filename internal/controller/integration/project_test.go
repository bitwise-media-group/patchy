// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package integration

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v89/github"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/ghclient"
	"github.com/bitwise-media-group/patchy/internal/kube"
)

// fakeTracker records every tracking-system write.
type fakeTracker struct {
	nextNumber int
	issues     map[int]*ghclient.Issue
	comments   []string
	assigned   []string
	closed     []int
	dismissed  []int
	bodyEdits  int
}

func newFakeTracker() *fakeTracker {
	return &fakeTracker{nextNumber: 7, issues: map[int]*ghclient.Issue{}}
}

func (f *fakeTracker) Create(
	_ context.Context, repo ghclient.Repo, req ghclient.IssueRequest,
) (*ghclient.Issue, error) {
	n := f.nextNumber
	f.nextNumber++
	is := &ghclient.Issue{Repo: repo, Number: n, Title: req.Title, Body: req.Body, State: "open", Labels: req.Labels}
	f.issues[n] = is
	return is, nil
}

func (f *fakeTracker) GetIssue(_ context.Context, _ ghclient.Repo, number int) (*ghclient.Issue, error) {
	is, ok := f.issues[number]
	if !ok {
		return nil, fmt.Errorf("ghclient: get issue #%d: %w", number,
			&github.ErrorResponse{Response: &http.Response{StatusCode: http.StatusNotFound}})
	}
	return is, nil
}

func (f *fakeTracker) EditBody(_ context.Context, _ ghclient.Repo, number int, body string) error {
	f.bodyEdits++
	f.issues[number].Body = body
	return nil
}

func (f *fakeTracker) AddLabels(_ context.Context, _ ghclient.Repo, number int, add []string) error {
	f.issues[number].Labels = append(f.issues[number].Labels, add...)
	return nil
}

func (f *fakeTracker) RemoveLabel(_ context.Context, _ ghclient.Repo, number int, name string) error {
	out := f.issues[number].Labels[:0]
	for _, l := range f.issues[number].Labels {
		if l != name {
			out = append(out, l)
		}
	}
	f.issues[number].Labels = out
	return nil
}

func (f *fakeTracker) Comment(_ context.Context, _ ghclient.Repo, _ int, body string) error {
	f.comments = append(f.comments, body)
	return nil
}

func (f *fakeTracker) Assign(_ context.Context, _ ghclient.Repo, _ int, logins []string) error {
	f.assigned = append(f.assigned, logins...)
	return nil
}

func (f *fakeTracker) Close(_ context.Context, _ ghclient.Repo, number int) error {
	f.closed = append(f.closed, number)
	return nil
}

func (f *fakeTracker) DismissAlert(_ context.Context, _ ghclient.Repo, number int, _, _ string) error {
	f.dismissed = append(f.dismissed, number)
	return nil
}

// projectable is a Finding ready for projection.
func projectable(phase v1alpha1.Phase) *v1alpha1.Finding {
	return &v1alpha1.Finding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "finding-aa-1", Namespace: "patchy",
			Labels: map[string]string{v1alpha1.LabelKeyHash: "aa"},
		},
		Spec: v1alpha1.FindingSpec{
			IntegrationRef: v1alpha1.LocalObjectReference{Name: "gh"},
			TrackingRef:    &v1alpha1.LocalObjectReference{Name: "gh"},
			Source:         "ghas",
			Repository: &v1alpha1.FindingRepository{
				Type: v1alpha1.RepositoryTypeGitHub,
				URL:  "https://github.com/acme/orders",
				Name: "acme/orders",
			},
			Advisories: []string{"CVE-2026-0001"},
			Title:      "Reflected XSS",
			Severity:   v1alpha1.LevelHigh,
			Alerts:     []v1alpha1.Alert{{ID: "42", URL: "https://github.com/acme/orders/security/42"}},
		},
		Status: v1alpha1.FindingStatus{Phase: phase},
	}
}

func newProjector(t *testing.T, tracker *fakeTracker, objs ...client.Object) (*FindingReconciler, client.Client) {
	t.Helper()
	c := fake.NewClientBuilder().
		WithScheme(kube.Scheme()).
		WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.Finding{}, &v1alpha1.Investigation{}, &v1alpha1.Remediation{}).
		Build()
	r := &FindingReconciler{
		Client:    c,
		Namespace: "patchy",
		Now:       func() time.Time { return testClock },
		ClientFor: func(context.Context, *v1alpha1.Integration, ghclient.Repo) (trackerClient, error) {
			return tracker, nil
		},
	}
	return r, c
}

func reconcileFinding(t *testing.T, r *FindingReconciler) {
	t.Helper()
	if _, err := r.Reconcile(t.Context(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "patchy", Name: "finding-aa-1"},
	}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
}

func TestProjectCreatesIssue(t *testing.T) {
	tracker := newFakeTracker()
	r, c := newProjector(t, tracker, testIntegration(), projectable(v1alpha1.PhaseOpened))

	reconcileFinding(t, r)

	f := get(t, c, "finding-aa-1")
	if f.Status.Tracking == nil || f.Status.Tracking.IssueNumber != 7 {
		t.Fatalf("tracking = %+v, want issue 7 linked", f.Status.Tracking)
	}
	if !strings.Contains(f.Status.Tracking.URL, "/acme/orders/issues/7") {
		t.Errorf("tracking url = %q", f.Status.Tracking.URL)
	}
	is := tracker.issues[7]
	if !strings.Contains(is.Body, "patchy:finding patchy/finding-aa-1") {
		t.Errorf("body missing finding marker:\n%s", is.Body)
	}
	if !strings.Contains(is.Title, "CVE-2026-0001") {
		t.Errorf("title = %q", is.Title)
	}
	found := false
	for _, l := range is.Labels {
		if l == "security-finding: opened" {
			found = true
		}
	}
	if !found {
		t.Errorf("labels = %v, want security-finding: opened", is.Labels)
	}
}

func TestProjectRerendersOnPhaseChange(t *testing.T) {
	tracker := newFakeTracker()
	r, c := newProjector(t, tracker, testIntegration(), projectable(v1alpha1.PhaseOpened))
	reconcileFinding(t, r)

	// Same state: no extra writes.
	reconcileFinding(t, r)
	if tracker.bodyEdits != 0 {
		t.Fatalf("bodyEdits = %d after idempotent reconcile, want 0", tracker.bodyEdits)
	}

	f := get(t, c, "finding-aa-1")
	f.Status.Phase = v1alpha1.PhaseEnhanced
	if err := c.Status().Update(t.Context(), f); err != nil {
		t.Fatalf("update phase: %v", err)
	}
	reconcileFinding(t, r)
	if tracker.bodyEdits != 1 {
		t.Errorf("bodyEdits = %d after phase change, want 1", tracker.bodyEdits)
	}
	labels := tracker.issues[7].Labels
	found := false
	for _, l := range labels {
		if l == "security-finding: enhanced" {
			found = true
		}
	}
	if !found {
		t.Errorf("labels = %v, want security-finding: enhanced", labels)
	}
}

func TestProjectReprojectsWhenIssueGone(t *testing.T) {
	tracker := newFakeTracker()
	r, c := newProjector(t, tracker, testIntegration(), projectable(v1alpha1.PhaseOpened))
	reconcileFinding(t, r)

	// The issue vanishes on the forge (deleted there, or an ephemeral
	// tracker was reset) and a phase change forces the projection to touch
	// it: the dead link is dropped, then a fresh issue is projected.
	delete(tracker.issues, 7)
	f := get(t, c, "finding-aa-1")
	f.Status.Phase = v1alpha1.PhaseEnhanced
	if err := c.Status().Update(t.Context(), f); err != nil {
		t.Fatalf("update phase: %v", err)
	}

	reconcileFinding(t, r)
	if f = get(t, c, "finding-aa-1"); f.Status.Tracking != nil {
		t.Fatalf("tracking = %+v after 404, want unlinked", f.Status.Tracking)
	}

	reconcileFinding(t, r)
	f = get(t, c, "finding-aa-1")
	if f.Status.Tracking == nil || f.Status.Tracking.IssueNumber != 8 {
		t.Fatalf("tracking = %+v, want fresh issue 8 linked", f.Status.Tracking)
	}
	if _, ok := tracker.issues[8]; !ok {
		t.Errorf("issue 8 not created; issues = %v", tracker.issues)
	}
}

func TestProjectEnrichmentCommentOnce(t *testing.T) {
	tracker := newFakeTracker()
	r, c := newProjector(t, tracker, testIntegration(), projectable(v1alpha1.PhaseOpened))
	reconcileFinding(t, r)

	f := get(t, c, "finding-aa-1")
	f.Status.Enrichments = []v1alpha1.Enrichment{{
		Enhancer: "static-context", Markdown: "owned by team-payments", AppliedAt: metav1.NewTime(testClock),
	}}
	if err := c.Status().Update(t.Context(), f); err != nil {
		t.Fatalf("add enrichment: %v", err)
	}
	reconcileFinding(t, r)
	reconcileFinding(t, r)

	n := 0
	for _, cm := range tracker.comments {
		if strings.Contains(cm, "patchy:enrichment static-context") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("enrichment comments = %d, want exactly 1", n)
	}
}

func TestProjectDismissed(t *testing.T) {
	tracker := newFakeTracker()
	fnd := projectable(v1alpha1.PhaseDismissed)
	fnd.Status.Tracking = &v1alpha1.TrackingStatus{Integration: "gh", IssueNumber: 7, URL: "u", State: "open"}
	tracker.issues[7] = &ghclient.Issue{Number: 7, State: "open"}
	r, c := newProjector(t, tracker, testIntegration(), fnd)

	reconcileFinding(t, r)

	if len(tracker.dismissed) != 1 || tracker.dismissed[0] != 42 {
		t.Errorf("dismissed = %v, want [42]", tracker.dismissed)
	}
	if len(tracker.closed) != 1 {
		t.Errorf("closed = %v, want the tracking issue", tracker.closed)
	}
	if got := get(t, c, "finding-aa-1").Status.Tracking.State; got != "closed" {
		t.Errorf("tracking state = %q, want closed", got)
	}
}

func TestProjectAwaitingApprovalNotifies(t *testing.T) {
	tracker := newFakeTracker()
	fnd := projectable(v1alpha1.PhaseAwaitingApproval)
	fnd.Status.Tracking = &v1alpha1.TrackingStatus{Integration: "gh", IssueNumber: 7, URL: "u", State: "open"}
	fnd.Status.Owners = []string{"alice", "bob"}
	tracker.issues[7] = &ghclient.Issue{Number: 7, State: "open"}
	r, _ := newProjector(t, tracker, testIntegration(), fnd)

	reconcileFinding(t, r)
	reconcileFinding(t, r) // idempotent

	notices := 0
	for _, cm := range tracker.comments {
		if strings.Contains(cm, "/approve") {
			notices++
		}
	}
	if notices != 1 {
		t.Errorf("approval notices = %d, want exactly 1", notices)
	}
	if len(tracker.assigned) != 2 {
		t.Errorf("assigned = %v, want alice+bob", tracker.assigned)
	}
}
