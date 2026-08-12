// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package evalapi

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/pkg/evaluation"
)

func runningEval(name string) *v1alpha1.Evaluation {
	e := terminalEval(name)
	e.Status.Phase = v1alpha1.EvaluationRunning
	e.Status.UnitsComplete = 0
	return e
}

func runningUnit(evalName string, index int32) *v1alpha1.EvaluationUnit {
	u := settledUnit(evalName, index)
	u.Status = v1alpha1.EvaluationUnitStatus{Phase: v1alpha1.RunRunning, Harness: "claude"}
	return u
}

// sseEvent is one named event block read off the stream; comment-only blocks
// (connected, pings) are skipped.
type sseEvent struct {
	name string
	data string
}

func nextSSEEvent(t *testing.T, sc *bufio.Scanner) sseEvent {
	t.Helper()
	var ev sseEvent
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if ev.name != "" {
				return ev
			}
		case strings.HasPrefix(line, "event: "):
			ev.name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			ev.data = strings.TrimPrefix(line, "data: ")
		}
	}
	t.Fatalf("stream ended waiting for an event (err %v)", sc.Err())
	return ev
}

func openMonitor(t *testing.T, srv *Server, name string) *bufio.Scanner {
	t.Helper()
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		ts.URL+"/api/v1/evaluations/"+name+"/events", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("X-Test-User", "dev")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET events: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("SSE = %d", resp.StatusCode)
	}
	return bufio.NewScanner(resp.Body)
}

func TestSSELiveUpdateThenEnd(t *testing.T) {
	srv, c, _ := newTestServer(t, runningEval("eval-live"), runningUnit("eval-live", 0))
	sc := openMonitor(t, srv, "eval-live")

	// Replay: the running unit goes out first.
	ev := nextSSEEvent(t, sc)
	if ev.name != evaluation.SSEEventUnit {
		t.Fatalf("first event = %q, want unit", ev.name)
	}
	var unit evaluation.UnitStatusWire
	if err := json.Unmarshal([]byte(ev.data), &unit); err != nil {
		t.Fatalf("decode unit: %v", err)
	}
	if unit.Phase != string(v1alpha1.RunRunning) {
		t.Fatalf("replayed phase = %q, want Running", unit.Phase)
	}

	// The unit settles and the evaluation completes; a broker wake re-reads.
	ctx := t.Context()
	var u v1alpha1.EvaluationUnit
	if err := c.Get(ctx, types.NamespacedName{Namespace: "patchy", Name: "eval-live-u000"}, &u); err != nil {
		t.Fatalf("get unit: %v", err)
	}
	u.Status.Phase = v1alpha1.RunComplete
	u.Status.CasesPassed = 2
	if err := c.Status().Update(ctx, &u); err != nil {
		t.Fatalf("update unit: %v", err)
	}
	var e v1alpha1.Evaluation
	if err := c.Get(ctx, types.NamespacedName{Namespace: "patchy", Name: "eval-live"}, &e); err != nil {
		t.Fatalf("get eval: %v", err)
	}
	e.Status.Phase = v1alpha1.EvaluationComplete
	e.Status.UnitsComplete = 1
	if err := c.Status().Update(ctx, &e); err != nil {
		t.Fatalf("update eval: %v", err)
	}
	srv.broker.publish("eval-live")

	// The changed unit re-emits, then the end event closes the stream.
	ev = nextSSEEvent(t, sc)
	if ev.name != evaluation.SSEEventUnit {
		t.Fatalf("post-update event = %q, want unit", ev.name)
	}
	if err := json.Unmarshal([]byte(ev.data), &unit); err != nil {
		t.Fatalf("decode unit: %v", err)
	}
	if unit.Phase != string(v1alpha1.RunComplete) {
		t.Errorf("updated phase = %q, want Complete", unit.Phase)
	}

	ev = nextSSEEvent(t, sc)
	if ev.name != evaluation.SSEEventEnd {
		t.Fatalf("final event = %q, want end", ev.name)
	}
	var final evaluation.EvaluationStatusWire
	if err := json.Unmarshal([]byte(ev.data), &final); err != nil {
		t.Fatalf("decode end: %v", err)
	}
	if final.Phase != string(v1alpha1.EvaluationComplete) || final.UnitsComplete != 1 {
		t.Errorf("end snapshot = %+v, want Complete with 1 unit complete", final)
	}
	for _, u := range final.Units {
		if u.Result != nil {
			t.Errorf("end snapshot unit %s carries a result, want it stripped", u.Name)
		}
	}
}

func TestSSEDeletedMidWatch(t *testing.T) {
	srv, c, _ := newTestServer(t, runningEval("eval-gone"), runningUnit("eval-gone", 0))
	sc := openMonitor(t, srv, "eval-gone")

	if ev := nextSSEEvent(t, sc); ev.name != evaluation.SSEEventUnit {
		t.Fatalf("first event = %q, want unit", ev.name)
	}

	// Cancelled (deleted) mid-watch: the monitor reports and finishes.
	e := &v1alpha1.Evaluation{ObjectMeta: metav1.ObjectMeta{Namespace: "patchy", Name: "eval-gone"}}
	if err := c.Delete(t.Context(), e); err != nil {
		t.Fatalf("delete eval: %v", err)
	}
	srv.broker.publish("eval-gone")

	ev := nextSSEEvent(t, sc)
	if ev.name != evaluation.SSEEventEnd {
		t.Fatalf("post-delete event = %q, want end", ev.name)
	}
	var final evaluation.EvaluationStatusWire
	if err := json.Unmarshal([]byte(ev.data), &final); err != nil {
		t.Fatalf("decode end: %v", err)
	}
	if final.Name != "eval-gone" || final.Phase != "" {
		t.Errorf("end snapshot = %+v, want the bare name", final)
	}
}

func TestBrokerPublishIsNonBlocking(t *testing.T) {
	b := newBroker()
	ch := b.subscribe("e")
	// Two publishes with no reader: the second finds the buffer full and
	// must not block.
	b.publish("e")
	b.publish("e")
	select {
	case <-ch:
	default:
		t.Fatal("no wake pending after publish")
	}
	b.unsubscribe("e", ch)
	b.publish("e") // no subscribers left; must not panic or block
}
