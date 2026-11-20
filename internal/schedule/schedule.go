// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package schedule ranks pending agent runs for bounded-concurrency slot
// grants. It is pure arithmetic: the scheduler reconcilers list candidates
// from the cluster, ask Pick for the winners, and write the grants. Slot
// policy is therefore fully table-testable, and slot accounting is always
// cluster state, never controller memory.
package schedule

import (
	"slices"
	"strings"
	"time"
)

// Candidate is one pending run.
type Candidate struct {
	// Name of the run object.
	Name string
	// Priority in [0, 100]; higher runs first.
	Priority int32
	// QueuedAt orders equal priorities first-in-first-out and drives aging.
	QueuedAt time.Time
	// Expedited candidates rank ahead of every non-expedited one regardless
	// of priority or aging (a human asked for the front of the queue);
	// expedited candidates order among themselves as usual.
	Expedited bool
}

// AgingPolicy lifts long-waiting candidates so a flood of high-priority work
// cannot starve low-priority findings forever, while the cap keeps critical
// items on top.
type AgingPolicy struct {
	// Interval grants one effective-priority point per elapsed interval.
	Interval time.Duration
	// Cap bounds the total aging boost.
	Cap int32
}

// effective is the candidate's aged priority at time now.
func (p AgingPolicy) effective(c Candidate, now time.Time) int32 {
	if p.Interval <= 0 || p.Cap <= 0 {
		return c.Priority
	}
	boost := min(int32(now.Sub(c.QueuedAt)/p.Interval), p.Cap)
	return c.Priority + max(boost, 0)
}

// Pick returns the names of the freeSlots highest-ranked candidates, ordered
// expedited first, then effective-priority descending, then QueuedAt
// ascending, then Name. The order is total and deterministic so concurrent
// deciders agree.
func Pick(pending []Candidate, freeSlots int, now time.Time, aging AgingPolicy) []string {
	if freeSlots <= 0 || len(pending) == 0 {
		return nil
	}
	ranked := slices.Clone(pending)
	slices.SortFunc(ranked, func(a, b Candidate) int {
		ea, eb := aging.effective(a, now), aging.effective(b, now)
		switch {
		case a.Expedited != b.Expedited:
			if a.Expedited {
				return -1
			}
			return 1
		case ea != eb:
			return int(eb - ea)
		case !a.QueuedAt.Equal(b.QueuedAt):
			return a.QueuedAt.Compare(b.QueuedAt)
		default:
			return strings.Compare(a.Name, b.Name)
		}
	})
	n := min(freeSlots, len(ranked))
	out := make([]string, 0, n)
	for _, c := range ranked[:n] {
		out = append(out, c.Name)
	}
	return out
}
