// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package integration

import (
	"fmt"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/kube"
	"github.com/bitwise-media-group/patchy/internal/webhook"
)

// trackedFinding is a Finding in the given phase whose tracking issue URL is
// linked, so the URL index resolves it.
func trackedFinding(phase v1alpha1.Phase) *v1alpha1.Finding {
	const name = "finding-aa-1"
	return &v1alpha1.Finding{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "patchy"},
		Spec: v1alpha1.FindingSpec{
			IntegrationRef: v1alpha1.LocalObjectReference{Name: "gh"},
			Source:         "ghas",
			Advisories:     []string{"CVE-2026-0001"},
		},
		Status: v1alpha1.FindingStatus{
			Phase: phase,
			Tracking: &v1alpha1.TrackingStatus{
				Integration: "gh",
				IssueNumber: 7,
				URL:         "https://github.com/acme/orders/issues/7",
				State:       "open",
			},
		},
	}
}

func newSignals(t *testing.T, objs ...client.Object) (*Signals, client.Client) {
	t.Helper()
	c := fake.NewClientBuilder().
		WithScheme(kube.Scheme()).
		WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.Finding{}).
		WithIndex(&v1alpha1.Finding{}, TrackingURLIndex, func(obj client.Object) []string {
			f := obj.(*v1alpha1.Finding)
			if f.Status.Tracking == nil {
				return nil
			}
			return []string{f.Status.Tracking.URL}
		}).
		Build()
	return &Signals{
		Client:    c,
		Namespace: "patchy",
		Now:       func() time.Time { return testClock },
	}, c
}

func event(typ, payload string) webhook.Event {
	return webhook.Event{Type: typ, Payload: []byte(payload)}
}

func TestSignalsIssueClosedHandsOff(t *testing.T) {
	s, c := newSignals(t, trackedFinding(v1alpha1.PhaseQueued))
	payload := `{"action":"closed","issue":{"number":7,"html_url":"https://github.com/acme/orders/issues/7"}}`
	if err := s.Handle(t.Context(), testIntegration(), event("issues", payload)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	f := get(t, c, "finding-aa-1")
	if f.Status.Phase != v1alpha1.PhaseHandedOff {
		t.Errorf("phase = %q, want HandedOff", f.Status.Phase)
	}
	if f.Status.Tracking.State != "closed" {
		t.Errorf("tracking state = %q, want closed", f.Status.Tracking.State)
	}
}

func TestSignalsIssueClosedTerminalNoop(t *testing.T) {
	s, c := newSignals(t, trackedFinding(v1alpha1.PhaseRemediated))
	payload := `{"action":"closed","issue":{"number":7,"html_url":"https://github.com/acme/orders/issues/7"}}`
	if err := s.Handle(t.Context(), testIntegration(), event("issues", payload)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := get(t, c, "finding-aa-1").Status.Phase; got != v1alpha1.PhaseRemediated {
		t.Errorf("phase = %q, want Remediated unchanged", got)
	}
}

func TestSignalsReopenAfterDismissal(t *testing.T) {
	s, c := newSignals(t, trackedFinding(v1alpha1.PhaseDismissed))
	payload := `{"action":"reopened","issue":{"number":7,"html_url":"https://github.com/acme/orders/issues/7"}}`
	if err := s.Handle(t.Context(), testIntegration(), event("issues", payload)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := get(t, c, "finding-aa-1").Status.Phase; got != v1alpha1.PhaseHandedOff {
		t.Errorf("phase = %q, want HandedOff (edge 19)", got)
	}
}

func TestSignalsApprove(t *testing.T) {
	cases := []struct {
		name        string
		association string
		body        string
		wantSet     bool
	}{
		{"collaborator approves", "COLLABORATOR", "/approve", true},
		{"owner approves with note", "OWNER", "/approve ship it", true},
		{"random user ignored", "NONE", "/approve", false},
		{"non-command ignored", "OWNER", "looks fine to me", false},
		{"prefix-only word ignored", "OWNER", "/approved", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, c := newSignals(t, trackedFinding(v1alpha1.PhaseAwaitingApproval))
			payload := fmt.Sprintf(
				`{"action":"created","issue":{"html_url":"https://github.com/acme/orders/issues/7"},`+
					`"comment":{"body":%q,"author_association":%q,"user":{"login":"dev"}}}`,
				tc.body, tc.association)
			if err := s.Handle(t.Context(), testIntegration(), event("issue_comment", payload)); err != nil {
				t.Fatalf("Handle: %v", err)
			}
			f := get(t, c, "finding-aa-1")
			if got := f.Spec.Approval != nil; got != tc.wantSet {
				t.Errorf("approval set = %v, want %v", got, tc.wantSet)
			}
			if tc.wantSet && f.Spec.Approval.By != "dev" {
				t.Errorf("approval.by = %q, want dev", f.Spec.Approval.By)
			}
		})
	}
}

func TestSignalsApproveStaleHandedOff(t *testing.T) {
	approvalAt := func(at time.Time) *v1alpha1.Approval {
		return &v1alpha1.Approval{By: "old-approver", At: metav1.NewTime(at)}
	}
	completed := metav1.NewTime(testClock.Add(-time.Hour))
	cases := []struct {
		name        string
		phase       v1alpha1.Phase
		approval    *v1alpha1.Approval
		completedAt *metav1.Time
		wantBy      string
	}{
		{
			// The recorded approval predates hand-off, so it can never
			// revive the finding; a fresh /approve replaces it.
			name:        "stale approval on HandedOff replaced",
			phase:       v1alpha1.PhaseHandedOff,
			approval:    approvalAt(testClock.Add(-2 * time.Hour)),
			completedAt: &completed,
			wantBy:      "dev",
		},
		{
			name:        "fresh approval on HandedOff kept",
			phase:       v1alpha1.PhaseHandedOff,
			approval:    approvalAt(testClock.Add(-30 * time.Minute)),
			completedAt: &completed,
			wantBy:      "old-approver",
		},
		{
			name:     "existing approval outside HandedOff kept",
			phase:    v1alpha1.PhaseAwaitingApproval,
			approval: approvalAt(testClock.Add(-2 * time.Hour)),
			wantBy:   "old-approver",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fnd := trackedFinding(tc.phase)
			fnd.Spec.Approval = tc.approval
			fnd.Status.CompletedAt = tc.completedAt
			s, c := newSignals(t, fnd)
			payload := `{"action":"created","issue":{"html_url":"https://github.com/acme/orders/issues/7"},` +
				`"comment":{"body":"/approve","author_association":"OWNER","user":{"login":"dev"}}}`
			if err := s.Handle(t.Context(), testIntegration(), event("issue_comment", payload)); err != nil {
				t.Fatalf("Handle: %v", err)
			}
			if got := get(t, c, "finding-aa-1").Spec.Approval.By; got != tc.wantBy {
				t.Errorf("approval.by = %q, want %q", got, tc.wantBy)
			}
		})
	}
}

func TestSignalsPullRequest(t *testing.T) {
	cases := []struct {
		name      string
		merged    bool
		wantPhase v1alpha1.Phase
		wantState string
	}{
		{"merged remediates", true, v1alpha1.PhaseRemediated, "merged"},
		{"closed unmerged fails", false, v1alpha1.PhaseFailed, "closed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fnd := trackedFinding(v1alpha1.PhaseInReview)
			fnd.Status.PullRequest = &v1alpha1.PullRequestStatus{Number: 11, State: "open"}
			s, c := newSignals(t, fnd)
			payload := fmt.Sprintf(
				`{"action":"closed","pull_request":{"number":11,"merged":%v,`+
					`"merged_at":"2026-07-21T13:00:00Z","head":{"ref":"patchy/finding-aa-1"}}}`,
				tc.merged)
			if err := s.Handle(t.Context(), testIntegration(), event("pull_request", payload)); err != nil {
				t.Fatalf("Handle: %v", err)
			}
			f := get(t, c, "finding-aa-1")
			if f.Status.Phase != tc.wantPhase {
				t.Errorf("phase = %q, want %q", f.Status.Phase, tc.wantPhase)
			}
			if f.Status.PullRequest.State != tc.wantState {
				t.Errorf("pr state = %q, want %q", f.Status.PullRequest.State, tc.wantState)
			}
			if tc.merged && (f.Status.CompletedAt == nil || f.Status.PullRequest.MergedAt == nil) {
				t.Error("merged PR left completedAt/mergedAt unset")
			}
		})
	}
}

func TestSignalsForeignIssueIgnored(t *testing.T) {
	s, _ := newSignals(t, trackedFinding(v1alpha1.PhaseQueued))
	payload := `{"action":"closed","issue":{"number":99,"html_url":"https://github.com/acme/other/issues/99"}}`
	if err := s.Handle(t.Context(), testIntegration(), event("issues", payload)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
}
