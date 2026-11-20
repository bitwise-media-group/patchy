// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package v1alpha1

import (
	"fmt"
	"slices"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Phase is the Finding lifecycle state. Queued is a real phase because
// remediation runs in priority order with bounded concurrency, so findings
// visibly wait for a slot.
// +kubebuilder:validation:Enum=Opened;Enhanced;Investigating;Queued;AwaitingApproval;Remediating;InReview;Remediated;Failed;Dismissed;HandedOff
type Phase string

// Finding phases.
const (
	PhaseOpened           Phase = "Opened"
	PhaseEnhanced         Phase = "Enhanced"
	PhaseInvestigating    Phase = "Investigating"
	PhaseQueued           Phase = "Queued"
	PhaseAwaitingApproval Phase = "AwaitingApproval"
	PhaseRemediating      Phase = "Remediating"
	PhaseInReview         Phase = "InReview"
	PhaseRemediated       Phase = "Remediated"
	PhaseFailed           Phase = "Failed"
	PhaseDismissed        Phase = "Dismissed"
	PhaseHandedOff        Phase = "HandedOff"
)

// transitions is the legal edge table. Every edge has exactly one writer
// component (enforced in review, documented per edge):
//
//   - integration-controller: ""→Opened (ingest), InReview→Remediated/Failed
//     (PR webhooks), Dismissed→HandedOff (issue reopened), and every
//     non-terminal→HandedOff (human closed the tracking issue).
//   - context-controller: Opened→Enhanced.
//   - investigation-controller: Enhanced→Investigating (child create is the
//     lease), Investigating→Enhanced (retry revert), and the verdict routing
//     Investigating→{Queued, AwaitingApproval, Dismissed, HandedOff, Failed}.
//   - investigation-controller (gate): Failed→Enhanced (human retry of a
//     failed investigation).
//   - remediation-controller: AwaitingApproval→Queued and HandedOff→Queued
//     (approval/revival), Failed→Queued (human retry of a failed
//     remediation/review), Queued→Remediating (scheduler grant),
//     Remediating→Queued (retry re-queue), Remediating→{InReview, HandedOff,
//     Failed}.
var transitions = map[Phase][]Phase{
	"":            {PhaseOpened},
	PhaseOpened:   {PhaseEnhanced, PhaseHandedOff},
	PhaseEnhanced: {PhaseInvestigating, PhaseHandedOff},
	PhaseInvestigating: {
		PhaseEnhanced, PhaseQueued, PhaseAwaitingApproval,
		PhaseDismissed, PhaseHandedOff, PhaseFailed,
	},
	PhaseQueued:           {PhaseRemediating, PhaseHandedOff},
	PhaseAwaitingApproval: {PhaseQueued, PhaseHandedOff},
	PhaseRemediating:      {PhaseQueued, PhaseInReview, PhaseHandedOff, PhaseFailed},
	PhaseInReview:         {PhaseRemediated, PhaseFailed, PhaseHandedOff},
	PhaseRemediated:       nil,
	PhaseFailed:           {PhaseEnhanced, PhaseQueued}, // human retry (see RetryTarget)
	PhaseDismissed:        {PhaseHandedOff},             // human reopened the tracking issue
	PhaseHandedOff:        {PhaseQueued},                // revival via /approve
}

// terminal is the set of phases that complete a Finding for TTL purposes
// (status.completedAt is set on entry). Dismissed and HandedOff keep outgoing
// edges because they are revivable terminals. Revival clears completedAt and
// cancels the TTL.
var terminal = map[Phase]bool{
	PhaseRemediated: true,
	PhaseFailed:     true,
	PhaseDismissed:  true,
	PhaseHandedOff:  true,
}

// CanTransition reports whether moving a Finding from phase `from` to `to` is
// legal. The empty phase means a new Finding. Self-transitions are always
// legal no-ops.
func CanTransition(from, to Phase) bool {
	if from == to {
		return true
	}
	return slices.Contains(transitions[from], to)
}

// Terminal reports whether the phase completes a Finding (starts its TTL).
// Dismissed, HandedOff, and Failed are terminal but revivable.
func Terminal(p Phase) bool {
	return terminal[p]
}

// RetryTarget returns the phase a Failed finding recovers to when a human
// retries it. That is the recoverable state immediately before the failure.
// It returns "" when the finding is not Failed or its history holds no
// retryable phase.
// The failed stage is re-attempted by the normal machinery from there: the
// investigation gate opens a fresh attempt from Enhanced, the remediation
// spawner from Queued.
func RetryTarget(f *Finding) Phase {
	if f.Status.Phase != PhaseFailed {
		return ""
	}
	prior := Phase("")
	for _, pt := range f.Status.PhaseTimes {
		if pt.Phase == PhaseFailed {
			continue // the entry (or an earlier failure) itself
		}
		prior = pt.Phase
	}
	switch prior {
	case PhaseInvestigating:
		return PhaseEnhanced
	case PhaseRemediating, PhaseInReview:
		return PhaseQueued
	}
	return ""
}

// RetryRequested reports whether spec.retry is actionable: present and newer
// than the failure's completion. A retry consumed by a transition (which
// clears completedAt) can never re-fire, because a later failure stamps a
// fresh completedAt that outdates it.
func RetryRequested(f *Finding) bool {
	r := f.Spec.Retry
	if r == nil {
		return false
	}
	done := f.Status.CompletedAt
	return done == nil || r.At.After(done.Time)
}

// SetPhase moves the Finding to phase `to` at time `now`: it validates the
// transition, appends to status.phaseTimes, and maintains status.completedAt
// (set on terminal entry and cleared on revival; the TTL contract is
// completedAt + TTL). Callers running under conflict retry must call SetPhase
// again after every re-Get so the transition is re-validated against fresh
// state; an illegal transition returns an error and mutates nothing.
func SetPhase(f *Finding, to Phase, now time.Time) error {
	from := f.Status.Phase
	if !CanTransition(from, to) {
		return fmt.Errorf("illegal finding transition %q -> %q", from, to)
	}
	if from == to {
		return nil
	}
	t := metav1.NewTime(now)
	f.Status.Phase = to
	f.Status.PhaseTimes = append(f.Status.PhaseTimes, PhaseTime{Phase: to, At: t})
	if Terminal(to) {
		f.Status.CompletedAt = &t
	} else {
		f.Status.CompletedAt = nil
	}
	return nil
}
