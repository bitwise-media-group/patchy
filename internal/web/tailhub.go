// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package web

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/bitwise-media-group/patchy/internal/transcript"
)

// Tailer follows a running agent Job's transcript; *jobs.Tailer implements it.
type Tailer interface {
	Tail(ctx context.Context, jobName string, fn func(transcript.Turn) error) error
}

// maxLiveTails bounds concurrent upstream log follows. Each one is an open
// watch against the API server, so the cap protects the apiserver rather than
// this process — viewers past it fall back to the persisted transcript.
const maxLiveTails = 8

// tailBufferBytes bounds the turn text a run holds for replay. It matches the
// recorder's default stream total (plus headroom for its closing notice), so a
// viewer joining late — or reconnecting — sees the whole conversation rather
// than only what arrives after they connect.
const tailBufferBytes = transcript.DefaultStreamMaxTotalBytes + 64<<10

// subscriberBuffer is how far a viewer may fall behind the follow before it is
// evicted and told to resume.
const subscriberBuffer = 256

// errTooManyTails is returned when the live-follow budget is exhausted.
var errTooManyTails = errors.New("too many live transcripts")

// tailHub multiplexes one upstream log follow per running Job across every
// viewer watching it, and replays what the run has already said to each new
// subscriber. Without it, ten people opening the same finding would open ten
// follows and each would see the conversation only from the moment they
// arrived.
type tailHub struct {
	tailer Tailer
	log    *slog.Logger

	mu   sync.Mutex
	runs map[string]*tailRun
}

func newTailHub(t Tailer, log *slog.Logger) *tailHub {
	return &tailHub{tailer: t, log: log, runs: make(map[string]*tailRun)}
}

// tailRun is one followed Job and its watchers.
type tailRun struct {
	cancel context.CancelFunc

	mu        sync.Mutex
	seen      []transcript.Turn
	seenBytes int
	subs      map[*subscriber]struct{}
	finished  bool
}

// subscriber is one viewer's delivery channel. lagged is set, before ch is
// closed, when the viewer fell too far behind and was evicted.
type subscriber struct {
	ch     chan transcript.Turn
	lagged atomic.Bool
}

// subscription is one viewer's view of a followed run.
type subscription struct {
	// Replay is everything the run said before this viewer arrived, after
	// the resume point the viewer asked for.
	Replay []transcript.Turn
	// Turns delivers what it says next. It closes when the run ends, or when
	// the viewer fell behind and was evicted — Lagged tells the two apart.
	Turns <-chan transcript.Turn

	hub     *tailHub
	jobName string
	sub     *subscriber
}

// Lagged reports whether Turns closed because this viewer could not keep up,
// rather than because the run ended. A lagged viewer must reconnect and resume
// from the last turn it saw; it was never silently skipped past a turn.
func (s *subscription) Lagged() bool { return s.sub.lagged.Load() }

// Close detaches the viewer, stopping the upstream follow when it was the last.
func (s *subscription) Close() { s.hub.unsubscribe(s.jobName, s.sub) }

// subscribe attaches a viewer to jobName, starting the follow if this is the
// first. It returns the conversation so far — the turns with Seq above after,
// so a resuming viewer is not sent what it already has — plus the channel
// carrying the rest, captured atomically so no turn falls between the two.
func (h *tailHub) subscribe(jobName string, after int) (*subscription, error) {
	if h == nil || h.tailer == nil {
		return nil, errTooManyTails
	}
	h.mu.Lock()
	run, ok := h.runs[jobName]
	if !ok {
		if len(h.runs) >= maxLiveTails {
			h.mu.Unlock()
			return nil, errTooManyTails
		}
		run = &tailRun{subs: make(map[*subscriber]struct{})}
		h.runs[jobName] = run
		ctx, cancel := context.WithCancel(context.Background())
		run.cancel = cancel
		go h.follow(ctx, jobName, run)
	}
	h.mu.Unlock()

	sub := &subscriber{ch: make(chan transcript.Turn, subscriberBuffer)}
	run.mu.Lock()
	replay := turnsAfter(run.seen, after)
	if run.finished {
		// The run ended between the hub lookup and here; the replay is the
		// whole conversation, so hand back a closed channel.
		run.mu.Unlock()
		close(sub.ch)
		return &subscription{Replay: replay, Turns: sub.ch, hub: h, jobName: jobName, sub: sub}, nil
	}
	run.subs[sub] = struct{}{}
	run.mu.Unlock()

	return &subscription{Replay: replay, Turns: sub.ch, hub: h, jobName: jobName, sub: sub}, nil
}

// turnsAfter copies the turns with Seq above after. Seq is increasing, so the
// cut is one binary search rather than a filter.
func turnsAfter(turns []transcript.Turn, after int) []transcript.Turn {
	i, _ := slices.BinarySearchFunc(turns, after+1, func(t transcript.Turn, seq int) int {
		return cmp.Compare(t.Seq, seq)
	})
	return append([]transcript.Turn(nil), turns[i:]...)
}

// follow drives the upstream log follow, buffering and fanning out each turn.
func (h *tailHub) follow(ctx context.Context, jobName string, run *tailRun) {
	err := h.tailer.Tail(ctx, jobName, func(t transcript.Turn) error {
		run.mu.Lock()
		if run.seenBytes+len(t.Text) <= tailBufferBytes {
			run.seen = append(run.seen, t)
			run.seenBytes += len(t.Text)
		}
		for sub := range run.subs {
			// Never block the follow on a slow viewer, and never skip a turn
			// on one either: a viewer that cannot keep up is evicted, and its
			// client resumes from the last turn it saw. A blocked follow
			// would cost everyone the rest; a skipped turn would be a silent
			// hole in the conversation.
			select {
			case sub.ch <- t:
			default:
				sub.lagged.Store(true)
				close(sub.ch)
				delete(run.subs, sub)
			}
		}
		run.mu.Unlock()
		return ctx.Err()
	})
	if err != nil && ctx.Err() == nil {
		h.log.LogAttrs(ctx, slog.LevelWarn, "transcript follow ended",
			slog.String("job", jobName), slog.Any("error", err))
	}

	run.mu.Lock()
	run.finished = true
	for sub := range run.subs {
		close(sub.ch)
		delete(run.subs, sub)
	}
	run.mu.Unlock()

	h.mu.Lock()
	if h.runs[jobName] == run {
		delete(h.runs, jobName)
	}
	h.mu.Unlock()
}

// unsubscribe detaches one viewer and cancels the follow when none remain.
func (h *tailHub) unsubscribe(jobName string, sub *subscriber) {
	h.mu.Lock()
	run, ok := h.runs[jobName]
	h.mu.Unlock()
	if !ok {
		return
	}

	run.mu.Lock()
	if _, live := run.subs[sub]; live {
		delete(run.subs, sub)
		close(sub.ch)
	}
	last := len(run.subs) == 0 && !run.finished
	run.mu.Unlock()

	if last {
		run.cancel() // follow() removes the run from the hub as it unwinds
	}
}

// activeTails reports how many follows are open (tests).
func (h *tailHub) activeTails() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.runs)
}
