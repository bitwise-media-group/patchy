// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package v1alpha1

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestCanTransition(t *testing.T) {
	cases := []struct {
		name string
		from Phase
		to   Phase
		want bool
	}{
		{"new finding opens", "", PhaseOpened, true},
		{"new finding cannot skip ahead", "", PhaseInvestigating, false},
		{"self transition is a no-op", PhaseQueued, PhaseQueued, true},
		{"opened enhances", PhaseOpened, PhaseEnhanced, true},
		{"enhanced investigates", PhaseEnhanced, PhaseInvestigating, true},
		{"enhanced cannot queue directly", PhaseEnhanced, PhaseQueued, false},
		{"investigation retry reverts", PhaseInvestigating, PhaseEnhanced, true},
		{"verdict remediate queues", PhaseInvestigating, PhaseQueued, true},
		{"verdict low-confidence holds", PhaseInvestigating, PhaseAwaitingApproval, true},
		{"verdict ignore dismisses", PhaseInvestigating, PhaseDismissed, true},
		{"verdict manual hands off", PhaseInvestigating, PhaseHandedOff, true},
		{"investigation exhausted fails", PhaseInvestigating, PhaseFailed, true},
		{"investigating cannot remediate directly", PhaseInvestigating, PhaseRemediating, false},
		{"approval queues", PhaseAwaitingApproval, PhaseQueued, true},
		{"approval cannot bypass the queue", PhaseAwaitingApproval, PhaseRemediating, false},
		{"scheduler grants a slot", PhaseQueued, PhaseRemediating, true},
		{"remediation retry re-queues", PhaseRemediating, PhaseQueued, true},
		{"remediation success reviews", PhaseRemediating, PhaseInReview, true},
		{"remediation unfixable hands off", PhaseRemediating, PhaseHandedOff, true},
		{"remediation exhausted fails", PhaseRemediating, PhaseFailed, true},
		{"pr merged remediates", PhaseInReview, PhaseRemediated, true},
		{"pr closed unmerged fails", PhaseInReview, PhaseFailed, true},
		{"human closes issue in review", PhaseInReview, PhaseHandedOff, true},
		{"human closes issue while queued", PhaseQueued, PhaseHandedOff, true},
		{"remediated is terminal", PhaseRemediated, PhaseHandedOff, false},
		{"failed retries to enhanced", PhaseFailed, PhaseEnhanced, true},
		{"failed retries to queued", PhaseFailed, PhaseQueued, true},
		{"failed cannot re-run directly", PhaseFailed, PhaseInvestigating, false},
		{"failed cannot hand off", PhaseFailed, PhaseHandedOff, false},
		{"dismissed revives on reopen", PhaseDismissed, PhaseHandedOff, true},
		{"dismissed cannot re-queue directly", PhaseDismissed, PhaseQueued, false},
		{"handed off revives to the queue", PhaseHandedOff, PhaseQueued, true},
		{"handed off cannot re-investigate", PhaseHandedOff, PhaseInvestigating, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := CanTransition(c.from, c.to); got != c.want {
				t.Errorf("CanTransition(%q, %q) = %v, want %v", c.from, c.to, got, c.want)
			}
		})
	}
}

func TestTerminal(t *testing.T) {
	cases := []struct {
		phase Phase
		want  bool
	}{
		{PhaseRemediated, true},
		{PhaseFailed, true},
		{PhaseDismissed, true},
		{PhaseHandedOff, true},
		{PhaseOpened, false},
		{PhaseEnhanced, false},
		{PhaseInvestigating, false},
		{PhaseQueued, false},
		{PhaseAwaitingApproval, false},
		{PhaseRemediating, false},
		{PhaseInReview, false},
		{"", false},
	}
	for _, c := range cases {
		t.Run(string(c.phase), func(t *testing.T) {
			if got := Terminal(c.phase); got != c.want {
				t.Errorf("Terminal(%q) = %v, want %v", c.phase, got, c.want)
			}
		})
	}
}

func TestSetPhase(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)

	t.Run("legal transition appends phase time", func(t *testing.T) {
		f := &Finding{}
		if err := SetPhase(f, PhaseOpened, now); err != nil {
			t.Fatalf("SetPhase(new, Opened) error = %v", err)
		}
		if f.Status.Phase != PhaseOpened {
			t.Errorf("phase = %q, want %q", f.Status.Phase, PhaseOpened)
		}
		if len(f.Status.PhaseTimes) != 1 || f.Status.PhaseTimes[0].Phase != PhaseOpened {
			t.Errorf("phaseTimes = %v, want one Opened entry", f.Status.PhaseTimes)
		}
		if f.Status.CompletedAt != nil {
			t.Errorf("completedAt = %v, want nil for non-terminal", f.Status.CompletedAt)
		}
	})

	t.Run("illegal transition mutates nothing", func(t *testing.T) {
		f := &Finding{}
		f.Status.Phase = PhaseOpened
		if err := SetPhase(f, PhaseRemediating, now); err == nil {
			t.Fatal("SetPhase(Opened, Remediating) error = nil, want error")
		}
		if f.Status.Phase != PhaseOpened || len(f.Status.PhaseTimes) != 0 {
			t.Errorf("finding mutated on illegal transition: phase=%q times=%v",
				f.Status.Phase, f.Status.PhaseTimes)
		}
	})

	t.Run("self transition is a silent no-op", func(t *testing.T) {
		f := &Finding{}
		f.Status.Phase = PhaseQueued
		if err := SetPhase(f, PhaseQueued, now); err != nil {
			t.Fatalf("SetPhase(Queued, Queued) error = %v", err)
		}
		if len(f.Status.PhaseTimes) != 0 {
			t.Errorf("phaseTimes = %v, want none for self transition", f.Status.PhaseTimes)
		}
	})

	t.Run("terminal entry sets completedAt", func(t *testing.T) {
		f := &Finding{}
		f.Status.Phase = PhaseInReview
		if err := SetPhase(f, PhaseRemediated, now); err != nil {
			t.Fatalf("SetPhase(InReview, Remediated) error = %v", err)
		}
		if f.Status.CompletedAt == nil || !f.Status.CompletedAt.Time.Equal(now) {
			t.Errorf("completedAt = %v, want %v", f.Status.CompletedAt, now)
		}
	})

	t.Run("revival clears completedAt", func(t *testing.T) {
		f := &Finding{}
		f.Status.Phase = PhaseInvestigating
		if err := SetPhase(f, PhaseHandedOff, now); err != nil {
			t.Fatalf("SetPhase(Investigating, HandedOff) error = %v", err)
		}
		if f.Status.CompletedAt == nil {
			t.Fatal("completedAt = nil after HandedOff, want set")
		}
		later := now.Add(time.Hour)
		if err := SetPhase(f, PhaseQueued, later); err != nil {
			t.Fatalf("SetPhase(HandedOff, Queued) error = %v", err)
		}
		if f.Status.CompletedAt != nil {
			t.Errorf("completedAt = %v after revival, want nil", f.Status.CompletedAt)
		}
		if len(f.Status.PhaseTimes) != 2 {
			t.Errorf("phaseTimes = %v, want two entries", f.Status.PhaseTimes)
		}
	})
}

func TestRetryTarget(t *testing.T) {
	at := metav1.NewTime(time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC))
	history := func(phases ...Phase) []PhaseTime {
		out := make([]PhaseTime, 0, len(phases))
		for _, p := range phases {
			out = append(out, PhaseTime{Phase: p, At: at})
		}
		return out
	}
	cases := []struct {
		name  string
		phase Phase
		times []PhaseTime
		want  Phase
	}{
		{"failed investigation recovers to enhanced", PhaseFailed,
			history(PhaseOpened, PhaseEnhanced, PhaseInvestigating, PhaseFailed), PhaseEnhanced},
		{"failed remediation recovers to queued", PhaseFailed,
			history(PhaseQueued, PhaseRemediating, PhaseFailed), PhaseQueued},
		{"closed pr recovers to queued", PhaseFailed,
			history(PhaseRemediating, PhaseInReview, PhaseFailed), PhaseQueued},
		{"repeated failures use the latest pre-failure phase", PhaseFailed,
			history(PhaseInvestigating, PhaseFailed, PhaseEnhanced, PhaseInvestigating,
				PhaseQueued, PhaseRemediating, PhaseFailed), PhaseQueued},
		{"not failed", PhaseInvestigating, history(PhaseInvestigating), ""},
		{"no history", PhaseFailed, nil, ""},
		{"unretryable prior phase", PhaseFailed, history(PhaseOpened, PhaseFailed), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &Finding{}
			f.Status.Phase = c.phase
			f.Status.PhaseTimes = c.times
			if got := RetryTarget(f); got != c.want {
				t.Errorf("RetryTarget() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestRetryRequested(t *testing.T) {
	base := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	completed := metav1.NewTime(base)
	fresh := metav1.NewTime(base.Add(time.Minute))
	stale := metav1.NewTime(base.Add(-time.Minute))

	cases := []struct {
		name      string
		retry     *ActionRequest
		completed *metav1.Time
		want      bool
	}{
		{"no retry", nil, &completed, false},
		{"fresh retry", &ActionRequest{By: "u", At: fresh}, &completed, true},
		{"stale retry after a newer failure", &ActionRequest{By: "u", At: stale}, &completed, false},
		{"no completion recorded", &ActionRequest{By: "u", At: stale}, nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &Finding{}
			f.Spec.Retry = c.retry
			f.Status.CompletedAt = c.completed
			if got := RetryRequested(f); got != c.want {
				t.Errorf("RetryRequested() = %v, want %v", got, c.want)
			}
		})
	}
}

// TestEveryEdgeReachable pins the shape of the table itself: every phase in
// the enum appears as a key, and every target of every edge is a known phase,
// so a typo in the table cannot silently orphan a phase.
func TestEveryEdgeReachable(t *testing.T) {
	known := map[Phase]bool{
		PhaseOpened: true, PhaseEnhanced: true, PhaseInvestigating: true,
		PhaseQueued: true, PhaseAwaitingApproval: true, PhaseRemediating: true,
		PhaseInReview: true, PhaseRemediated: true, PhaseFailed: true,
		PhaseDismissed: true, PhaseHandedOff: true,
	}
	for p := range known {
		if _, ok := transitions[p]; !ok {
			t.Errorf("phase %q missing from the transition table", p)
		}
	}
	for from, tos := range transitions {
		if from != "" && !known[from] {
			t.Errorf("transition table keys unknown phase %q", from)
		}
		for _, to := range tos {
			if !known[to] {
				t.Errorf("edge %q -> %q targets an unknown phase", from, to)
			}
		}
	}
}
