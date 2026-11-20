// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package web

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bitwise-media-group/patchy/internal/transcript"
)

// countingTailer records how many follows were opened and streams on demand.
type countingTailer struct {
	follows atomic.Int32

	mu   sync.Mutex
	emit func(transcript.Turn) error
	once sync.Once     // live is signalled by the first follow only
	live chan struct{} // closed once a follow is delivering
	stop chan struct{}
}

func newCountingTailer() *countingTailer {
	return &countingTailer{live: make(chan struct{}), stop: make(chan struct{})}
}

func (c *countingTailer) Tail(ctx context.Context, _ string, fn func(transcript.Turn) error) error {
	c.follows.Add(1)
	c.mu.Lock()
	c.emit = fn
	c.mu.Unlock()
	c.once.Do(func() { close(c.live) })

	select {
	case <-c.stop:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// send pushes a turn through the active follow.
func (c *countingTailer) send(t transcript.Turn) {
	c.mu.Lock()
	fn := c.emit
	c.mu.Unlock()
	if fn != nil {
		_ = fn(t)
	}
}

func testHub(t Tailer) *tailHub {
	return newTailHub(t, slog.New(slog.DiscardHandler))
}

func TestHubSharesOneFollowAcrossViewers(t *testing.T) {
	// Ten people opening the same finding must not open ten watches against
	// the API server.
	tl := newCountingTailer()
	h := testHub(tl)

	subs := make([]*subscription, 3)
	for i := range subs {
		sub, err := h.subscribe("job-1", 0)
		if err != nil {
			t.Fatalf("subscribe %d: %v", i, err)
		}
		subs[i] = sub
	}
	defer func() {
		for _, s := range subs {
			s.Close()
		}
	}()

	<-tl.live
	if got := tl.follows.Load(); got != 1 {
		t.Errorf("opened %d follows for 3 viewers, want 1", got)
	}

	tl.send(transcript.Turn{Seq: 1, Kind: transcript.KindText, Text: "hello"})
	for i, sub := range subs {
		select {
		case turn := <-sub.Turns:
			if turn.Text != "hello" {
				t.Errorf("viewer %d got %+v", i, turn)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("viewer %d received nothing", i)
		}
	}
}

func TestHubReplaysToLateViewer(t *testing.T) {
	// A viewer opening a run already 50 turns in should see all 50, not just
	// what arrives after they connect.
	tl := newCountingTailer()
	h := testHub(tl)

	first, err := h.subscribe("job-1", 0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer first.Close()
	<-tl.live

	tl.send(transcript.Turn{Seq: 1, Kind: transcript.KindText, Text: "one"})
	tl.send(transcript.Turn{Seq: 2, Kind: transcript.KindText, Text: "two"})
	<-first.Turns
	<-first.Turns

	late, err := h.subscribe("job-1", 0)
	if err != nil {
		t.Fatalf("late subscribe: %v", err)
	}
	defer late.Close()

	if len(late.Replay) != 2 {
		t.Fatalf("replay = %+v, want both earlier turns", late.Replay)
	}
	if late.Replay[0].Text != "one" || late.Replay[1].Text != "two" {
		t.Errorf("replay = %+v", late.Replay)
	}

	// And it still receives what comes next.
	tl.send(transcript.Turn{Seq: 3, Kind: transcript.KindText, Text: "three"})
	select {
	case turn := <-late.Turns:
		if turn.Text != "three" {
			t.Errorf("late viewer got %+v, want three", turn)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("late viewer received nothing after replay")
	}
}

func TestHubStopsFollowWhenLastViewerLeaves(t *testing.T) {
	// A leaked follow is an open watch against the API server for the rest of
	// the run.
	tl := newCountingTailer()
	h := testHub(tl)

	a, err := h.subscribe("job-1", 0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	b, err := h.subscribe("job-1", 0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	<-tl.live

	a.Close()
	if h.activeTails() != 1 {
		t.Error("follow stopped while a viewer was still watching")
	}
	b.Close()
	waitFor(t, func() bool { return h.activeTails() == 0 })
}

func TestHubCapsConcurrentFollows(t *testing.T) {
	tl := newCountingTailer()
	h := testHub(tl)

	subs := make([]*subscription, 0, maxLiveTails)
	for i := range maxLiveTails {
		sub, err := h.subscribe(jobName(i), 0)
		if err != nil {
			t.Fatalf("subscribe %d: %v", i, err)
		}
		subs = append(subs, sub)
	}
	defer func() {
		for _, s := range subs {
			s.Close()
		}
	}()

	if _, err := h.subscribe("one-too-many", 0); err == nil {
		t.Errorf("subscribe past the cap of %d succeeded, want an error", maxLiveTails)
	}
}

func TestHubClosesChannelWhenRunEnds(t *testing.T) {
	tl := newCountingTailer()
	h := testHub(tl)

	sub, err := h.subscribe("job-1", 0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Close()
	<-tl.live

	close(tl.stop) // the agent container exited
	select {
	case _, ok := <-sub.Turns:
		if ok {
			t.Error("received a turn after the run ended")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("channel not closed when the run ended")
	}
}

func TestHubWithoutTailerRefuses(t *testing.T) {
	// A deployment with no reach into the agents namespace still serves
	// persisted transcripts; it just cannot follow live ones.
	h := testHub(nil)
	if _, err := h.subscribe("job-1", 0); err == nil {
		t.Error("subscribe without a tailer succeeded, want an error")
	}
}

func jobName(i int) string { return "job-" + string(rune('a'+i)) }

func TestHubReplayIsNotCappedByTurnCount(t *testing.T) {
	// The recorder no longer caps turns, so neither may the replay: a viewer
	// joining a long run late still gets the whole conversation.
	tl := newCountingTailer()
	h := testHub(tl)

	first, err := h.subscribe("job-1", 0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer first.Close()
	<-tl.live

	const n = 2000
	for i := 1; i <= n; i++ {
		tl.send(transcript.Turn{Seq: i, Kind: transcript.KindText, Text: "x"})
	}

	late, err := h.subscribe("job-1", 0)
	if err != nil {
		t.Fatalf("late subscribe: %v", err)
	}
	defer late.Close()
	if len(late.Replay) != n {
		t.Fatalf("replay has %d turns, want %d", len(late.Replay), n)
	}
	if late.Replay[n-1].Seq != n {
		t.Errorf("last replayed Seq = %d, want %d", late.Replay[n-1].Seq, n)
	}
}

func TestHubEvictsLaggedViewerWithoutSkipping(t *testing.T) {
	// A viewer that stops reading must not stall the follow, and must not be
	// silently skipped past a turn either: it is evicted, told so, and
	// everything it did receive is contiguous.
	tl := newCountingTailer()
	h := testHub(tl)

	slow, err := h.subscribe("job-1", 0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer slow.Close()
	<-tl.live

	for i := 1; i <= subscriberBuffer+10; i++ {
		tl.send(transcript.Turn{Seq: i, Kind: transcript.KindText, Text: "x"})
	}

	want := 1
	for turn := range slow.Turns {
		if turn.Seq != want {
			t.Fatalf("got Seq %d, want %d: a turn was skipped", turn.Seq, want)
		}
		want++
	}
	if got := want - 1; got != subscriberBuffer {
		t.Errorf("received %d turns before eviction, want %d", got, subscriberBuffer)
	}
	if !slow.Lagged() {
		t.Error("Lagged = false after eviction; the handler would send end and the client would stop")
	}
}

func TestHubRunEndIsNotLagged(t *testing.T) {
	tl := newCountingTailer()
	h := testHub(tl)
	sub, err := h.subscribe("job-1", 0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Close()
	<-tl.live
	close(tl.stop)
	for range sub.Turns {
	}
	if sub.Lagged() {
		t.Error("Lagged = true when the run simply ended")
	}
}

func TestHubResumeSkipsSeenTurns(t *testing.T) {
	tl := newCountingTailer()
	h := testHub(tl)

	first, err := h.subscribe("job-1", 0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer first.Close()
	<-tl.live
	for i := 1; i <= 5; i++ {
		tl.send(transcript.Turn{Seq: i, Kind: transcript.KindText, Text: "x"})
	}

	tests := []struct {
		after int
		want  []int
	}{
		{after: 0, want: []int{1, 2, 3, 4, 5}},
		{after: 3, want: []int{4, 5}},
		{after: 5, want: nil},
		{after: 99, want: nil},
	}
	for _, tt := range tests {
		resumed, err := h.subscribe("job-1", tt.after)
		if err != nil {
			t.Fatalf("subscribe after %d: %v", tt.after, err)
		}
		var got []int
		for _, turn := range resumed.Replay {
			got = append(got, turn.Seq)
		}
		resumed.Close()
		if !slices.Equal(got, tt.want) {
			t.Errorf("after %d: replay = %v, want %v", tt.after, got, tt.want)
		}
	}
}
