// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package web

import (
	"context"
	"errors"
	"log/slog"
	"sync"

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

// tailBuffer bounds the turns a run holds for replay. It matches the
// recorder's own turn cap, so a viewer joining late still sees the whole
// conversation rather than only what arrives after they connect.
const tailBuffer = 500

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

	mu       sync.Mutex
	seen     []transcript.Turn
	subs     map[chan transcript.Turn]struct{}
	finished bool
}

// subscription is one viewer's view of a followed run.
type subscription struct {
	// Replay is everything the run said before this viewer arrived.
	Replay []transcript.Turn
	// Turns delivers what it says next, and closes when the run ends.
	Turns <-chan transcript.Turn

	hub     *tailHub
	jobName string
	ch      chan transcript.Turn
}

// Close detaches the viewer, stopping the upstream follow when it was the last.
func (s *subscription) Close() { s.hub.unsubscribe(s.jobName, s.ch) }

// subscribe attaches a viewer to jobName, starting the follow if this is the
// first. It returns the conversation so far plus the channel carrying the rest,
// captured atomically so no turn falls between the two.
func (h *tailHub) subscribe(jobName string) (*subscription, error) {
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
		run = &tailRun{subs: make(map[chan transcript.Turn]struct{})}
		h.runs[jobName] = run
		ctx, cancel := context.WithCancel(context.Background())
		run.cancel = cancel
		go h.follow(ctx, jobName, run)
	}
	h.mu.Unlock()

	ch := make(chan transcript.Turn, 64)
	run.mu.Lock()
	replay := append([]transcript.Turn(nil), run.seen...)
	if run.finished {
		// The run ended between the hub lookup and here; the replay is the
		// whole conversation, so hand back a closed channel.
		run.mu.Unlock()
		close(ch)
		return &subscription{Replay: replay, Turns: ch, hub: h, jobName: jobName, ch: ch}, nil
	}
	run.subs[ch] = struct{}{}
	run.mu.Unlock()

	return &subscription{Replay: replay, Turns: ch, hub: h, jobName: jobName, ch: ch}, nil
}

// follow drives the upstream log follow, buffering and fanning out each turn.
func (h *tailHub) follow(ctx context.Context, jobName string, run *tailRun) {
	err := h.tailer.Tail(ctx, jobName, func(t transcript.Turn) error {
		run.mu.Lock()
		if len(run.seen) < tailBuffer {
			run.seen = append(run.seen, t)
		}
		for ch := range run.subs {
			// Never block the follow on a slow viewer: a dropped turn costs
			// that viewer one line, a blocked follow costs everyone the rest.
			select {
			case ch <- t:
			default:
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
	for ch := range run.subs {
		close(ch)
		delete(run.subs, ch)
	}
	run.mu.Unlock()

	h.mu.Lock()
	if h.runs[jobName] == run {
		delete(h.runs, jobName)
	}
	h.mu.Unlock()
}

// unsubscribe detaches one viewer and cancels the follow when none remain.
func (h *tailHub) unsubscribe(jobName string, ch chan transcript.Turn) {
	h.mu.Lock()
	run, ok := h.runs[jobName]
	h.mu.Unlock()
	if !ok {
		return
	}

	run.mu.Lock()
	if _, live := run.subs[ch]; live {
		delete(run.subs, ch)
		close(ch)
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
