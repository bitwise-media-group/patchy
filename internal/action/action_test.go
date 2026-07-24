// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package action

import (
	"errors"
	"slices"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
)

// testClock anchors every relative timestamp in this file.
var testClock = time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)

func at(d time.Duration) metav1.Time { return metav1.NewTime(testClock.Add(d)) }

// finding is a minimal Finding in the given phase.
func finding(phase v1alpha1.Phase, mutate ...func(*v1alpha1.Finding)) *v1alpha1.Finding {
	f := &v1alpha1.Finding{
		ObjectMeta: metav1.ObjectMeta{Name: "fnd-1", Namespace: "patchy"},
		Spec: v1alpha1.FindingSpec{
			IntegrationRef: v1alpha1.LocalObjectReference{Name: "gh"},
			Source:         "github-code-scanning",
			Advisories:     []string{"CVE-2026-0001"},
		},
		Status: v1alpha1.FindingStatus{Phase: phase},
	}
	for _, m := range mutate {
		m(f)
	}
	return f
}

// failed builds a Failed finding whose phase history recovers to target.
func failed(from v1alpha1.Phase, mutate ...func(*v1alpha1.Finding)) *v1alpha1.Finding {
	done := at(-time.Hour)
	return finding(v1alpha1.PhaseFailed, append([]func(*v1alpha1.Finding){func(f *v1alpha1.Finding) {
		f.Status.CompletedAt = &done
		f.Status.PhaseTimes = []v1alpha1.PhaseTime{
			{Phase: from, At: done},
			{Phase: v1alpha1.PhaseFailed, At: done},
		}
	}}, mutate...)...)
}

// allPhases is every phase in the enum, so the tables below can assert the
// unavailable set exhaustively rather than by sampling.
var allPhases = []v1alpha1.Phase{
	v1alpha1.PhaseOpened, v1alpha1.PhaseEnhanced, v1alpha1.PhaseInvestigating,
	v1alpha1.PhaseQueued, v1alpha1.PhaseAwaitingApproval, v1alpha1.PhaseRemediating,
	v1alpha1.PhaseInReview, v1alpha1.PhaseRemediated, v1alpha1.PhaseFailed,
	v1alpha1.PhaseDismissed, v1alpha1.PhaseHandedOff,
}

// applyCase is one Apply scenario runApplyCases drives.
type applyCase struct {
	name        string
	finding     *v1alpha1.Finding
	verb        string
	wantChanged bool
	wantErr     error
	check       func(t *testing.T, f *v1alpha1.Finding)
}

// runApplyCases applies each case as a fully granted operator and asserts the
// returned (changed, err) pair plus whatever the case wants to see on the
// mutated finding.
func runApplyCases(t *testing.T, cases []applyCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			changed, err := Apply(tc.finding, tc.verb, "op@acme.test", "note", testClock)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if changed != tc.wantChanged {
				t.Errorf("changed = %v, want %v", changed, tc.wantChanged)
			}
			if tc.check != nil {
				tc.check(t, tc.finding)
			}
		})
	}
}

func TestApplySuspendResume(t *testing.T) {
	runApplyCases(t, []applyCase{
		{
			name:        "suspend non-terminal",
			finding:     finding(v1alpha1.PhaseQueued),
			verb:        VerbSuspend,
			wantChanged: true,
			check: func(t *testing.T, f *v1alpha1.Finding) {
				if !f.Spec.Suspend {
					t.Error("spec.suspend not set")
				}
			},
		},
		{
			name:    "suspend terminal",
			finding: finding(v1alpha1.PhaseRemediated),
			verb:    VerbSuspend,
			wantErr: ErrUnavailable,
		},
		{
			name: "suspend already suspended is a no-op",
			finding: finding(v1alpha1.PhaseQueued, func(f *v1alpha1.Finding) {
				f.Spec.Suspend = true
			}),
			verb: VerbSuspend,
		},
		{
			name: "resume suspended",
			finding: finding(v1alpha1.PhaseQueued, func(f *v1alpha1.Finding) {
				f.Spec.Suspend = true
			}),
			verb:        VerbResume,
			wantChanged: true,
			check: func(t *testing.T, f *v1alpha1.Finding) {
				if f.Spec.Suspend {
					t.Error("spec.suspend not cleared")
				}
			},
		},
		{
			// Resume stays available on a terminal finding: a suspension
			// outlives the phase that motivated it, and refusing to lift one
			// would strand the finding.
			name: "resume terminal suspended",
			finding: finding(v1alpha1.PhaseHandedOff, func(f *v1alpha1.Finding) {
				f.Spec.Suspend = true
			}),
			verb:        VerbResume,
			wantChanged: true,
		},
		{
			name:    "resume not suspended is a no-op",
			finding: finding(v1alpha1.PhaseQueued),
			verb:    VerbResume,
		},
	})
}

func TestApplyApprove(t *testing.T) {
	runApplyCases(t, []applyCase{
		{
			name:        "approve awaiting approval",
			finding:     finding(v1alpha1.PhaseAwaitingApproval),
			verb:        VerbApprove,
			wantChanged: true,
			check: func(t *testing.T, f *v1alpha1.Finding) {
				if f.Spec.Approval == nil || f.Spec.Approval.By != "op@acme.test" {
					t.Errorf("spec.approval = %+v", f.Spec.Approval)
				}
				if f.Spec.Approval.Note != "note" {
					t.Errorf("note = %q, want %q", f.Spec.Approval.Note, "note")
				}
				// The phase edge belongs to remediation-controller.
				if f.Status.Phase != v1alpha1.PhaseAwaitingApproval {
					t.Errorf("phase moved to %q", f.Status.Phase)
				}
			},
		},
		{
			name:        "approve handed off",
			finding:     finding(v1alpha1.PhaseHandedOff),
			verb:        VerbApprove,
			wantChanged: true,
		},
		{
			name:    "approve outside the approval phases",
			finding: finding(v1alpha1.PhaseInvestigating),
			verb:    VerbApprove,
			wantErr: ErrUnavailable,
		},
		{
			// The spawner's revival gate wants an approval newer than
			// completedAt, so one older than it can never revive the finding
			// and must be replaced rather than honoured.
			name: "approve replaces a stale handed-off approval",
			finding: finding(v1alpha1.PhaseHandedOff, func(f *v1alpha1.Finding) {
				done := at(-time.Hour)
				f.Spec.Approval = &v1alpha1.Approval{By: "old", At: at(-2 * time.Hour)}
				f.Status.CompletedAt = &done
			}),
			verb:        VerbApprove,
			wantChanged: true,
			check: func(t *testing.T, f *v1alpha1.Finding) {
				if f.Spec.Approval.By != "op@acme.test" {
					t.Errorf("approval.by = %q, want replacement", f.Spec.Approval.By)
				}
			},
		},
		{
			name: "approve keeps a fresh approval",
			finding: finding(v1alpha1.PhaseHandedOff, func(f *v1alpha1.Finding) {
				done := at(-time.Hour)
				f.Spec.Approval = &v1alpha1.Approval{By: "old", At: at(-30 * time.Minute)}
				f.Status.CompletedAt = &done
			}),
			verb: VerbApprove,
			check: func(t *testing.T, f *v1alpha1.Finding) {
				if f.Spec.Approval.By != "old" {
					t.Errorf("approval.by = %q, want first approval kept", f.Spec.Approval.By)
				}
			},
		},
	})
}

func TestApplyRetryExpedite(t *testing.T) {
	runApplyCases(t, []applyCase{
		{
			name:        "retry a failed investigation",
			finding:     failed(v1alpha1.PhaseInvestigating),
			verb:        VerbRetry,
			wantChanged: true,
			check: func(t *testing.T, f *v1alpha1.Finding) {
				if f.Spec.Retry == nil || f.Spec.Retry.By != "op@acme.test" {
					t.Errorf("spec.retry = %+v", f.Spec.Retry)
				}
				if f.Status.Phase != v1alpha1.PhaseFailed {
					t.Errorf("phase moved to %q", f.Status.Phase)
				}
			},
		},
		{
			name:        "retry a failed remediation",
			finding:     failed(v1alpha1.PhaseRemediating),
			verb:        VerbRetry,
			wantChanged: true,
		},
		{
			name:    "retry outside failed",
			finding: finding(v1alpha1.PhaseQueued),
			verb:    VerbRetry,
			wantErr: ErrUnavailable,
		},
		{
			// Failed with no recoverable phase in its history: RetryTarget
			// returns "", so there is nothing to recover to.
			name:    "retry failed without retryable history",
			finding: finding(v1alpha1.PhaseFailed),
			verb:    VerbRetry,
			wantErr: ErrUnavailable,
		},
		{
			name: "retry keeps a pending request",
			finding: failed(v1alpha1.PhaseRemediating, func(f *v1alpha1.Finding) {
				f.Spec.Retry = &v1alpha1.ActionRequest{By: "old", At: at(-30 * time.Minute)}
			}),
			verb: VerbRetry,
			check: func(t *testing.T, f *v1alpha1.Finding) {
				if f.Spec.Retry.By != "old" {
					t.Errorf("retry.by = %q, want pending request kept", f.Spec.Retry.By)
				}
			},
		},
		{
			// A retry consumed by an earlier transition is outdated by the
			// completedAt of the failure that followed it, so a fresh failure
			// re-opens the action.
			name: "retry replaces a consumed request after a new failure",
			finding: failed(v1alpha1.PhaseInvestigating, func(f *v1alpha1.Finding) {
				f.Spec.Retry = &v1alpha1.ActionRequest{By: "old", At: at(-2 * time.Hour)}
			}),
			verb:        VerbRetry,
			wantChanged: true,
			check: func(t *testing.T, f *v1alpha1.Finding) {
				if f.Spec.Retry.By != "op@acme.test" {
					t.Errorf("retry.by = %q, want replacement", f.Spec.Retry.By)
				}
			},
		},
		{
			name:        "expedite queued",
			finding:     finding(v1alpha1.PhaseQueued),
			verb:        VerbExpedite,
			wantChanged: true,
			check: func(t *testing.T, f *v1alpha1.Finding) {
				if f.Spec.Expedite == nil || f.Spec.Expedite.By != "op@acme.test" {
					t.Errorf("spec.expedite = %+v", f.Spec.Expedite)
				}
			},
		},
		{
			name:    "expedite an in-flight remediation",
			finding: finding(v1alpha1.PhaseRemediating),
			verb:    VerbExpedite,
			wantErr: ErrUnavailable,
		},
		{
			name:    "expedite terminal",
			finding: finding(v1alpha1.PhaseDismissed),
			verb:    VerbExpedite,
			wantErr: ErrUnavailable,
		},
		{
			// Expedite is standing for the finding's whole lifetime, so the
			// first request wins even once the phase has moved past the ones
			// that would accept a new one.
			name: "expedite already expedited is a no-op",
			finding: finding(v1alpha1.PhaseRemediating, func(f *v1alpha1.Finding) {
				f.Spec.Expedite = &v1alpha1.ActionRequest{By: "old", At: at(-2 * time.Hour)}
			}),
			verb: VerbExpedite,
			check: func(t *testing.T, f *v1alpha1.Finding) {
				if f.Spec.Expedite.By != "old" {
					t.Errorf("expedite.by = %q, want first request kept", f.Spec.Expedite.By)
				}
			},
		},
		{
			name:    "unknown verb",
			finding: finding(v1alpha1.PhaseQueued),
			verb:    "escalate",
			wantErr: ErrUnknownVerb,
		},
		{
			name:    "admin verbs are not per-finding actions",
			finding: finding(v1alpha1.PhaseQueued),
			verb:    VerbReset,
			wantErr: ErrUnknownVerb,
		},
	})
}

// TestApplyNeverWritesStatus guards the single-writer contract: a human action
// records intent in spec, and the phase-owning controller is what moves the
// finding. An action that wrote status would give a phase edge two writers.
func TestApplyNeverWritesStatus(t *testing.T) {
	for _, verb := range ActionVerbs {
		for _, phase := range allPhases {
			f := failed(v1alpha1.PhaseRemediating)
			f.Status.Phase = phase
			before := f.Status.DeepCopy()
			if _, err := Apply(f, verb, "op@acme.test", "note", testClock); err != nil &&
				!errors.Is(err, ErrUnavailable) {
				t.Fatalf("%s/%s: unexpected error %v", verb, phase, err)
			}
			if !equalStatus(before, &f.Status) {
				t.Errorf("%s/%s: status mutated", verb, phase)
			}
		}
	}
}

// equalStatus compares the status fields an action could plausibly disturb.
func equalStatus(a, b *v1alpha1.FindingStatus) bool {
	if a.Phase != b.Phase || len(a.PhaseTimes) != len(b.PhaseTimes) {
		return false
	}
	if (a.CompletedAt == nil) != (b.CompletedAt == nil) {
		return false
	}
	return a.CompletedAt == nil || a.CompletedAt.Equal(b.CompletedAt)
}

func TestAvailable(t *testing.T) {
	cases := []struct {
		name    string
		finding *v1alpha1.Finding
		want    []string
	}{
		{
			name:    "fresh finding can only be expedited or suspended",
			finding: finding(v1alpha1.PhaseOpened),
			want:    []string{VerbExpedite, VerbSuspend},
		},
		{
			name:    "awaiting approval offers approve",
			finding: finding(v1alpha1.PhaseAwaitingApproval),
			want:    []string{VerbApprove, VerbExpedite, VerbSuspend},
		},
		{
			name:    "in-flight remediation cannot be expedited",
			finding: finding(v1alpha1.PhaseRemediating),
			want:    []string{VerbSuspend},
		},
		{
			name:    "failed offers retry",
			finding: failed(v1alpha1.PhaseInvestigating),
			want:    []string{VerbRetry},
		},
		{
			name:    "terminal finding offers nothing",
			finding: finding(v1alpha1.PhaseRemediated),
			want:    nil,
		},
		{
			name: "suspended finding offers resume instead of suspend",
			finding: finding(v1alpha1.PhaseQueued, func(f *v1alpha1.Finding) {
				f.Spec.Suspend = true
			}),
			want: []string{VerbExpedite, VerbResume},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Available(tc.finding, testClock); !slices.Equal(got, tc.want) {
				t.Errorf("Available = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestAvailableDoesNotMutate proves the probe runs against copies — Available
// is called on every finding in a list render, so a mutating probe would
// silently corrupt whatever the caller goes on to display or write.
func TestAvailableDoesNotMutate(t *testing.T) {
	f := finding(v1alpha1.PhaseAwaitingApproval)
	before := f.DeepCopy()
	Available(f, testClock)
	if f.Spec.Approval != nil || f.Spec.Suspend != before.Spec.Suspend ||
		f.Spec.Expedite != nil || f.Spec.Retry != nil {
		t.Errorf("Available mutated the finding: %+v", f.Spec)
	}
}

// TestAvailableAgreesWithApply keeps the two entry points honest across the
// whole phase space: whatever Available advertises must actually apply.
func TestAvailableAgreesWithApply(t *testing.T) {
	for _, phase := range allPhases {
		for _, suspended := range []bool{false, true} {
			f := finding(phase, func(f *v1alpha1.Finding) { f.Spec.Suspend = suspended })
			offered := Available(f, testClock)
			for _, verb := range ActionVerbs {
				changed, err := Apply(f.DeepCopy(), verb, "op@acme.test", "", testClock)
				want := err == nil && changed
				if got := slices.Contains(offered, verb); got != want {
					t.Errorf("phase %s suspended=%v: Available has %s = %v, Apply says %v",
						phase, suspended, verb, got, want)
				}
			}
		}
	}
}
