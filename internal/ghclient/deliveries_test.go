// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package ghclient

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
)

func TestDeliveries(t *testing.T) {
	mux, app := newFakeApp(t)
	mux.HandleFunc("GET /app/hook/deliveries", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("cursor") == "c2" {
			writeJSON(t, w, `[
				{"id": 3, "guid": "g3", "delivered_at": "2026-07-22T09:00:00Z", "status_code": 502, "event": "issues"}
			]`)
			return
		}
		w.Header().Set("Link", `<`+r.URL.Path+`?cursor=c2>; rel="next"`)
		writeJSON(t, w, `[
			{"id": 1, "guid": "g1", "delivered_at": "2026-07-22T11:00:00Z", "status_code": 202,
			 "event": "code_scanning_alert"},
			{"id": 2, "guid": "g1", "delivered_at": "2026-07-22T10:00:00Z", "status_code": 503,
			 "redelivery": true, "event": "code_scanning_alert"}
		]`)
	})

	var got []Delivery
	complete, err := app.Deliveries(context.Background(), func(d Delivery) bool {
		got = append(got, d)
		return true
	})
	if err != nil {
		t.Fatalf("Deliveries() error = %v", err)
	}
	if !complete {
		t.Error("complete = false, want true")
	}
	if len(got) != 3 {
		t.Fatalf("visited %d deliveries, want 3", len(got))
	}
	if !got[0].OK || got[0].GUID != "g1" || got[0].Event != "code_scanning_alert" {
		t.Errorf("first = %+v", got[0])
	}
	if got[1].OK || !got[1].Redelivery {
		t.Errorf("second = %+v, want failed redelivery", got[1])
	}
	if got[2].ID != 3 || got[2].OK {
		t.Errorf("third = %+v, want failed id 3", got[2])
	}
}

func TestDeliveriesStopsEarly(t *testing.T) {
	mux, app := newFakeApp(t)
	var pages atomic.Int32
	mux.HandleFunc("GET /app/hook/deliveries", func(w http.ResponseWriter, r *http.Request) {
		pages.Add(1)
		w.Header().Set("Link", `<`+r.URL.Path+`?cursor=next>; rel="next"`)
		writeJSON(t, w, `[{"id": 1, "guid": "g1", "delivered_at": "2026-07-22T11:00:00Z", "status_code": 200}]`)
	})

	visited := 0
	complete, err := app.Deliveries(context.Background(), func(Delivery) bool {
		visited++
		return false
	})
	if err != nil {
		t.Fatalf("Deliveries() error = %v", err)
	}
	if !complete || visited != 1 || pages.Load() != 1 {
		t.Errorf("complete=%v visited=%d pages=%d, want stop after the first entry", complete, visited, pages.Load())
	}
}

func TestDeliveriesPageCap(t *testing.T) {
	mux, app := newFakeApp(t)
	mux.HandleFunc("GET /app/hook/deliveries", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", `<`+r.URL.Path+`?cursor=next>; rel="next"`)
		writeJSON(t, w, `[{"id": 1, "guid": "g1", "delivered_at": "2026-07-22T11:00:00Z", "status_code": 200}]`)
	})

	complete, err := app.Deliveries(context.Background(), func(Delivery) bool { return true })
	if err != nil {
		t.Fatalf("Deliveries() error = %v", err)
	}
	if complete {
		t.Error("complete = true on an endless log, want page-cap truncation")
	}
}

func TestRedeliver(t *testing.T) {
	mux, app := newFakeApp(t)
	var hit atomic.Bool
	mux.HandleFunc("POST /app/hook/deliveries/42/attempts", func(w http.ResponseWriter, _ *http.Request) {
		hit.Store(true)
		w.WriteHeader(http.StatusAccepted)
		writeJSON(t, w, `{}`)
	})
	if err := app.Redeliver(context.Background(), 42); err != nil {
		t.Fatalf("Redeliver() error = %v", err)
	}
	if !hit.Load() {
		t.Error("redeliver endpoint was not called")
	}
}
