// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package web

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// waitFor polls until cond holds or the deadline passes.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not reached in time")
}

func TestBrokerPublish(t *testing.T) {
	b := newBroker()
	ch := b.subscribe()
	defer b.unsubscribe(ch)
	b.publish(eventFindingsChanged)
	select {
	case got := <-ch:
		if got != eventFindingsChanged {
			t.Errorf("event = %q, want %q", got, eventFindingsChanged)
		}
	case <-time.After(time.Second):
		t.Fatal("no event delivered")
	}
}

func TestBrokerPublishDoesNotBlock(t *testing.T) {
	b := newBroker()
	ch := b.subscribe()
	defer b.unsubscribe(ch)
	// Overfill the buffer; publish must drop, not block.
	done := make(chan struct{})
	go func() {
		for range 20 {
			b.publish(eventFindingsChanged)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("publish blocked on a full client buffer")
	}
}

func TestBrokerUnsubscribeIdempotentish(t *testing.T) {
	b := newBroker()
	ch := b.subscribe()
	b.unsubscribe(ch)
	if got := b.count(); got != 0 {
		t.Errorf("count = %d, want 0", got)
	}
	// A publish after unsubscribe must not panic on the closed channel.
	b.publish(eventFindingsChanged)
}

func TestHandleEventsStream(t *testing.T) {
	s := testServer(t)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	res, err := http.Get(ts.URL + "/events")
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if got := res.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content type = %q", got)
	}

	waitFor(t, func() bool { return s.broker.count() == 1 })
	s.broker.publish(eventFindingsChanged)

	scanner := bufio.NewScanner(res.Body)
	deadline := time.AfterFunc(2*time.Second, func() { _ = res.Body.Close() })
	defer deadline.Stop()
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), "event: "+eventFindingsChanged) {
			return
		}
	}
	t.Fatal("stream ended without the findings-changed event")
}
