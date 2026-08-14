// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package web

import (
	"fmt"
	"net/http"
	"sync"
	"time"
)

// SSE event names the status page listens for; each triggers a refetch of
// its own dataset.
const (
	// eventFindingsChanged: the findings/rollups projection changed.
	eventFindingsChanged = "findings-changed"
	// eventConfigChanged: the Forge/Integration configuration changed.
	eventConfigChanged = "config-changed"
)

// broker fans a notification out to every connected SSE client. Sends are
// non-blocking: a client whose buffer is full simply misses an intermediate
// notification, which is harmless because every notification carries the
// same meaning ("refetch") — the next one catches it up.
type broker struct {
	mu      sync.Mutex
	clients map[chan string]struct{}
}

func newBroker() *broker {
	return &broker{clients: make(map[chan string]struct{})}
}

// subscribe registers a new client and returns its event channel.
func (b *broker) subscribe() chan string {
	ch := make(chan string, 8)
	b.mu.Lock()
	b.clients[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

// unsubscribe removes and closes a client's channel. Safe to call once.
func (b *broker) unsubscribe(ch chan string) {
	b.mu.Lock()
	if _, ok := b.clients[ch]; ok {
		delete(b.clients, ch)
		close(ch)
	}
	b.mu.Unlock()
}

// publish delivers one named notification to every subscriber, dropping it
// for any client whose buffer is full rather than blocking the watcher —
// harmless, because every notification of one name carries the same
// meaning ("refetch") and the next one catches the client up.
func (b *broker) publish(event string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.clients {
		select {
		case ch <- event:
		default:
		}
	}
}

// count reports the number of connected clients.
func (b *broker) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.clients)
}

// keepalivePeriod bounds how long an idle SSE connection waits before a
// comment ping, so proxies and load balancers do not reap it.
const keepalivePeriod = 25 * time.Second

// handleEvents is the Server-Sent Events stream. It is public like the
// rollups endpoint: the only information it carries is that something
// changed. It emits a named event per published notification plus periodic
// comment pings to hold the connection open.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")

	ch := s.broker.subscribe()
	defer s.broker.unsubscribe(ch)

	_, _ = fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	ping := time.NewTicker(keepalivePeriod)
	defer ping.Stop()
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-ch:
			if !ok {
				return
			}
			_, _ = fmt.Fprintf(w, "event: %s\ndata: {}\n\n", event)
			flusher.Flush()
		case <-ping.C:
			_, _ = fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}
