// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package broker

import (
	"net/http"
	"strings"
	"sync"
	"time"
)

// ssePing is the idle keep-alive frame: an SSE comment line, spec-legal and
// invisible to every client parser.
const ssePing = ": ping\n\n"

// sseWriter injects keep-alive pings into an event-stream response while the
// upstream is silent — long thinking pauses on Bedrock/Vertex would otherwise
// let intermediaries idle-close the connection. The timer arms when the
// response declares Content-Type: text/event-stream and resets on every real
// byte; pings write only between upstream writes, under the same lock.
//
// Every write AND flush is serialized by mu: the reverse proxy flushes via
// http.ResponseController, which finds FlushError here before unwrapping, so
// its write-then-flush sequence and the timer goroutine's ping can never
// touch the underlying connection concurrently. Unwrap stays for the
// controller's other capabilities (deadlines, hijack).
type sseWriter struct {
	http.ResponseWriter
	interval time.Duration

	mu     sync.Mutex
	timer  *time.Timer
	closed bool
}

func newSSEWriter(w http.ResponseWriter, interval time.Duration) *sseWriter {
	return &sseWriter{ResponseWriter: w, interval: interval}
}

func (w *sseWriter) WriteHeader(code int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ResponseWriter.WriteHeader(code)
	ct := w.Header().Get("Content-Type")
	if w.interval > 0 && strings.HasPrefix(ct, "text/event-stream") {
		w.timer = time.AfterFunc(w.interval, w.ping)
	}
}

func (w *sseWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.timer != nil {
		w.timer.Reset(w.interval)
	}
	return w.ResponseWriter.Write(p)
}

// FlushError serializes the proxy's flushes with the ping goroutine's
// writes; http.ResponseController prefers it over unwrapping.
func (w *sseWriter) FlushError() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.flushLocked()
}

func (w *sseWriter) flushLocked() error {
	return http.NewResponseController(w.ResponseWriter).Flush()
}

// ping writes one comment frame and flushes it, then re-arms.
func (w *sseWriter) ping() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	if _, err := w.ResponseWriter.Write([]byte(ssePing)); err != nil {
		return
	}
	_ = w.flushLocked()
	w.timer.Reset(w.interval)
}

// stop disarms the timer once the response is done; no ping writes after it.
func (w *sseWriter) stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	if w.timer != nil {
		w.timer.Stop()
	}
}

func (w *sseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
