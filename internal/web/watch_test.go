// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package web

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
)

func TestDebounceCoalesces(t *testing.T) {
	s := testServer(t)
	s.debounce = 20 * time.Millisecond
	ch := s.broker.subscribe()
	defer s.broker.unsubscribe(ch)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	signal := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		s.debounceLoop(ctx, signal, eventFindingsChanged)
		close(done)
	}()

	// A burst of signals coalesces into one publish.
	for range 5 {
		select {
		case signal <- struct{}{}:
		default:
		}
	}
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("no publish after signals")
	}
	select {
	case <-ch:
		t.Fatal("burst produced a second publish")
	case <-time.After(100 * time.Millisecond):
	}

	// A later signal publishes again.
	signal <- struct{}{}
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("no publish after a fresh signal")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("debounce loop did not stop with the context")
	}
}

func TestResourceChanged(t *testing.T) {
	at := func(rv string) *v1alpha1.Finding {
		return &v1alpha1.Finding{ObjectMeta: metav1.ObjectMeta{Name: "f", ResourceVersion: rv}}
	}
	cases := []struct {
		name     string
		oldObj   any
		newObj   any
		wantFire bool
	}{
		{"same resourceVersion is a resync", at("1"), at("1"), false},
		{"moved resourceVersion fires", at("1"), at("2"), true},
		{"non-object fires defensively", "x", at("2"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resourceChanged(tc.oldObj, tc.newObj); got != tc.wantFire {
				t.Errorf("resourceChanged = %v, want %v", got, tc.wantFire)
			}
		})
	}
}
