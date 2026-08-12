// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package evalapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	toolscache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/pkg/evaluation"
)

// keepalivePeriod paces SSE comment pings so intermediaries keep the
// connection open.
const keepalivePeriod = 25 * time.Second

// resyncPeriod is the safety re-list for a connected monitor, in case a
// notification is lost.
const resyncPeriod = 30 * time.Second

// broker fans "this evaluation changed" notifications out to its monitors,
// keyed by evaluation name. Payloads are re-read from the cluster on wake —
// the channel carries no data, so a slow client only ever misses
// intermediate states, never the final one.
type broker struct {
	mu   sync.Mutex
	subs map[string]map[chan struct{}]struct{}
}

func newBroker() *broker {
	return &broker{subs: make(map[string]map[chan struct{}]struct{})}
}

func (b *broker) subscribe(name string) chan struct{} {
	ch := make(chan struct{}, 1)
	b.mu.Lock()
	if b.subs[name] == nil {
		b.subs[name] = make(map[chan struct{}]struct{})
	}
	b.subs[name][ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *broker) unsubscribe(name string, ch chan struct{}) {
	b.mu.Lock()
	if set, ok := b.subs[name]; ok {
		delete(set, ch)
		if len(set) == 0 {
			delete(b.subs, name)
		}
	}
	b.mu.Unlock()
}

// publish wakes the evaluation's monitors (non-blocking; a full buffer means
// a wake is already pending).
func (b *broker) publish(name string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs[name] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// StartWatch wires informer events into the broker. Registered as a manager
// runnable; blocks until ctx is done.
func (s *Server) StartWatch(ctx context.Context, c cache.Cache) error {
	notify := func(obj any) {
		o, ok := obj.(client.Object)
		if !ok {
			return
		}
		switch o.(type) {
		case *v1alpha1.Evaluation:
			s.broker.publish(o.GetName())
		case *v1alpha1.EvaluationUnit:
			if eval := o.GetLabels()[v1alpha1.LabelEvaluation]; eval != "" {
				s.broker.publish(eval)
			}
		}
	}
	for _, obj := range []client.Object{&v1alpha1.Evaluation{}, &v1alpha1.EvaluationUnit{}} {
		informer, err := c.GetInformer(ctx, obj)
		if err != nil {
			return fmt.Errorf("evalapi: informer: %w", err)
		}
		if _, err := informer.AddEventHandler(toolscache.ResourceEventHandlerFuncs{
			AddFunc:    notify,
			UpdateFunc: func(_, newObj any) { notify(newObj) },
			DeleteFunc: notify,
		}); err != nil {
			return fmt.Errorf("evalapi: event handler: %w", err)
		}
	}
	<-ctx.Done()
	return nil
}

// handleEvents is the SSE monitor: replay every unit, re-emit on change,
// finish with an explicit end event carrying the final snapshot.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorize(w, r, VerbGet); !ok {
		return
	}
	name := r.PathValue("name")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	snap, found, err := s.snapshot(r.Context(), name, true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "reading the evaluation failed")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "no such evaluation")
		return
	}

	notify := s.broker.subscribe(name)
	defer s.broker.unsubscribe(name, notify)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	// sent dedupes per-unit emissions across replays: a unit is re-sent only
	// when its observable state changed.
	sent := make(map[string]string)
	emit := func(snap *evaluation.EvaluationStatusWire) bool {
		for i := range snap.Units {
			u := &snap.Units[i]
			fp := unitFingerprint(u)
			if sent[u.Name] == fp {
				continue
			}
			if !writeSSE(w, evaluation.SSEEventUnit, u) {
				return false
			}
			sent[u.Name] = fp
		}
		flusher.Flush()
		return true
	}

	if !emit(snap) {
		return
	}
	if s.finishIfTerminal(w, flusher, snap) {
		return
	}

	keepalive := time.NewTicker(keepalivePeriod)
	defer keepalive.Stop()
	resync := time.NewTicker(resyncPeriod)
	defer resync.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			_, _ = fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case <-notify:
		case <-resync.C:
		}

		snap, found, err = s.snapshot(r.Context(), name, true)
		if err != nil {
			return
		}
		if !found {
			// Deleted (cancelled or TTL'd) mid-watch: report what we know
			// and finish.
			_ = writeSSE(w, evaluation.SSEEventEnd, evaluation.EvaluationStatusWire{Name: name})
			flusher.Flush()
			return
		}
		if !emit(snap) {
			return
		}
		if s.finishIfTerminal(w, flusher, snap) {
			return
		}
	}
}

// finishIfTerminal emits the end event when the evaluation settled.
func (s *Server) finishIfTerminal(w http.ResponseWriter, flusher http.Flusher,
	snap *evaluation.EvaluationStatusWire) bool {
	if snap.Phase != string(v1alpha1.EvaluationComplete) && snap.Phase != string(v1alpha1.EvaluationFailed) {
		return false
	}
	// The end snapshot omits per-unit results — every settled unit already
	// went out as its own event; resending entries would double the stream.
	final := *snap
	final.Units = make([]evaluation.UnitStatusWire, len(snap.Units))
	for i, u := range snap.Units {
		u.Result = nil
		final.Units[i] = u
	}
	_ = writeSSE(w, evaluation.SSEEventEnd, final)
	flusher.Flush()
	return true
}

// unitFingerprint captures the observable state of a unit wire for dedupe.
func unitFingerprint(u *evaluation.UnitStatusWire) string {
	return fmt.Sprintf("%s|%s|%t|%t", u.Phase, u.Reason, u.Summary != nil, u.Result != nil)
}

// writeSSE writes one named SSE event; false on a write error (client gone).
func writeSSE(w http.ResponseWriter, event string, v any) bool {
	raw, err := json.Marshal(v)
	if err != nil {
		return false
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, raw)
	return err == nil
}
