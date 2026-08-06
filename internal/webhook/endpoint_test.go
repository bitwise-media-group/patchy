// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// An endpoint that cannot authenticate or label its deliveries is a
// misconfiguration worth failing at startup, not one to discover when an
// unauthenticated request is accepted.
func TestNewServerRejectsIncompleteEndpoints(t *testing.T) {
	ok := githubEndpoint([]byte("s"), HandlerFunc(func(context.Context, Event) error { return nil }))

	without := func(mutate func(*Endpoint)) []Endpoint {
		e := ok
		mutate(&e)
		return []Endpoint{e}
	}

	for _, tt := range []struct {
		name      string
		endpoints []Endpoint
	}{
		{"no endpoints at all", nil},
		{"no path", without(func(e *Endpoint) { e.Path = "" })},
		{"no authenticator", without(func(e *Endpoint) { e.Auth = nil })},
		{"no decoder", without(func(e *Endpoint) { e.Decode = nil })},
		{"no handler", without(func(e *Endpoint) { e.Handler = nil })},
		// Two endpoints on one path would panic in the mux at serve time.
		{"duplicate path", []Endpoint{ok, ok}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewServer(Config{Endpoints: tt.endpoints}, testLog); err == nil {
				t.Error("NewServer() = nil error, want a configuration failure")
			}
		})
	}
}

// The point of the endpoint model: two providers with entirely different
// authentication and labelling share one listener, one queue, and one dedup
// window without knowing about each other.
func TestMultipleEndpoints(t *testing.T) {
	secret := []byte("s3cret")

	var mu sync.Mutex
	var got []Event
	received := make(chan struct{}, 8)
	record := HandlerFunc(func(_ context.Context, e Event) error {
		mu.Lock()
		got = append(got, e)
		mu.Unlock()
		received <- struct{}{}
		return nil
	})

	// A second provider: bearer-token auth, event type fixed by the route,
	// delivery id read out of the body.
	const token = "push-token"
	bearer := Endpoint{
		Path: "/cloud/webhooks",
		Auth: AuthenticatorFunc(func(_ context.Context, r *http.Request, _ []byte) error {
			if r.Header.Get("Authorization") != "Bearer "+token {
				return ErrUnauthenticated
			}
			return nil
		}),
		Decode: func(_ *http.Request, body []byte) (string, string, error) {
			var m struct {
				Message struct {
					ID string `json:"messageId"`
				} `json:"message"`
			}
			if err := json.Unmarshal(body, &m); err != nil {
				return "", "", err
			}
			return "cloud.notification", m.Message.ID, nil
		},
		Handler: record,
	}

	url, stop := startServer(t, Config{
		Endpoints: []Endpoint{githubEndpoint(secret, record), bearer},
	})
	defer stop()

	body := []byte(`{"message":{"messageId":"m-1"}}`)
	if s := post(t, url+"/cloud/webhooks",
		map[string]string{"Authorization": "Bearer " + token}, body); s != http.StatusAccepted {
		t.Fatalf("authenticated push = %d, want 202", s)
	}
	<-received

	// The GitHub route's HMAC must not admit the bearer token, nor the
	// reverse: each endpoint authenticates on its own terms.
	if s := post(t, url+testPath,
		map[string]string{"Authorization": "Bearer " + token}, body); s != http.StatusUnauthorized {
		t.Errorf("bearer token on the HMAC route = %d, want 401", s)
	}
	if s := post(t, url+"/cloud/webhooks", map[string]string{
		"X-Hub-Signature-256": sign(secret, body),
	}, body); s != http.StatusUnauthorized {
		t.Errorf("HMAC signature on the bearer route = %d, want 401", s)
	}

	// Delivery ids are scoped per route: the same id on another endpoint is a
	// different delivery, not a duplicate.
	ghBody := []byte(`{"action":"created"}`)
	if s := post(t, url+testPath, map[string]string{
		"X-Hub-Signature-256": sign(secret, ghBody),
		"X-GitHub-Event":      "code_scanning_alert",
		"X-GitHub-Delivery":   "m-1",
	}, ghBody); s != http.StatusAccepted {
		t.Fatalf("github delivery reusing the id = %d, want 202", s)
	}
	select {
	case <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("github delivery never handled; its id was deduped against another route's")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("handled %d events, want 2", len(got))
	}
	byPath := map[string]Event{}
	for _, e := range got {
		byPath[e.Path] = e
	}
	if e := byPath["/cloud/webhooks"]; e.Type != "cloud.notification" || e.DeliveryID != "m-1" {
		t.Errorf("push event = %+v, want the decoder's type and body-derived id", e)
	}
	if e := byPath[testPath]; e.Type != "code_scanning_alert" {
		t.Errorf("github event = %+v, want its header-derived type", e)
	}
}

// A wildcard route serves many integrations from one endpoint: the path
// segment picks the candidate secret, the event carries the concrete request
// path, and delivery ids are scoped per concrete path rather than per route.
func TestWildcardEndpoint(t *testing.T) {
	secrets := map[string][]byte{
		"warehouse": []byte("w-secret"),
		"cmdb":      []byte("c-secret"),
	}

	var mu sync.Mutex
	var got []Event
	received := make(chan struct{}, 8)
	record := HandlerFunc(func(_ context.Context, e Event) error {
		mu.Lock()
		got = append(got, e)
		mu.Unlock()
		received <- struct{}{}
		return nil
	})

	wildcard := Endpoint{
		Path: "/generic/{name}/webhooks",
		Auth: &HMACAuthenticator{
			Header: "X-Patchy-Signature-256",
			// SecretsFor is a decoy: SecretsForRequest must supersede it.
			SecretsFor: func(context.Context) [][]byte { return [][]byte{[]byte("decoy")} },
			SecretsForRequest: func(_ context.Context, r *http.Request) [][]byte {
				s, ok := secrets[r.PathValue("name")]
				if !ok {
					return nil
				}
				return [][]byte{s}
			},
		},
		Decode: func(r *http.Request, _ []byte) (string, string, error) {
			return "generic.findings", r.Header.Get("X-Patchy-Delivery"), nil
		},
		Handler: record,
	}

	url, stop := startServer(t, Config{Endpoints: []Endpoint{wildcard}})
	defer stop()

	body := []byte(`{"version":"v1","event":"findings"}`)
	deliver := func(name, id string, secret []byte) int {
		return post(t, url+"/generic/"+name+"/webhooks", map[string]string{
			"X-Patchy-Signature-256": sign(secret, body),
			"X-Patchy-Delivery":      id,
		}, body)
	}

	if s := deliver("warehouse", "d-1", secrets["warehouse"]); s != http.StatusAccepted {
		t.Fatalf("signed delivery = %d, want 202", s)
	}
	<-received

	// The same id on another integration's path is a different delivery.
	if s := deliver("cmdb", "d-1", secrets["cmdb"]); s != http.StatusAccepted {
		t.Fatalf("same id on another integration = %d, want 202", s)
	}
	select {
	case <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("delivery never handled; its id was deduped against another integration's")
	}

	// The same id on the same path is a duplicate.
	if s := deliver("warehouse", "d-1", secrets["warehouse"]); s != http.StatusAccepted {
		t.Fatalf("duplicate delivery = %d, want 202", s)
	}

	// One integration's secret must not admit a delivery addressed to
	// another, an unknown name yields no candidates at all, and the decoy
	// SecretsFor set proves SecretsForRequest supersedes it.
	if s := deliver("cmdb", "d-2", secrets["warehouse"]); s != http.StatusUnauthorized {
		t.Errorf("cross-integration secret = %d, want 401", s)
	}
	if s := deliver("unknown", "d-3", secrets["warehouse"]); s != http.StatusUnauthorized {
		t.Errorf("unknown integration = %d, want 401", s)
	}
	if s := deliver("warehouse", "d-4", []byte("decoy")); s != http.StatusUnauthorized {
		t.Errorf("SecretsFor decoy admitted = %d, want 401 (SecretsForRequest must supersede)", s)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("handled %d events, want 2", len(got))
	}
	paths := map[string]bool{}
	for _, e := range got {
		paths[e.Path] = true
	}
	if !paths["/generic/warehouse/webhooks"] || !paths["/generic/cmdb/webhooks"] {
		t.Errorf("event paths = %v, want the concrete request paths", paths)
	}
}

// A decoder that cannot make sense of the body is a 400: the delivery
// authenticated, so the caller is trusted enough to be told it sent rubbish.
func TestDecoderFailureIsBadRequest(t *testing.T) {
	url, stop := startServer(t, Config{Endpoints: []Endpoint{{
		Path:   "/x",
		Auth:   AuthenticatorFunc(func(context.Context, *http.Request, []byte) error { return nil }),
		Decode: func(*http.Request, []byte) (string, string, error) { return "", "", errors.New("nope") },
		Handler: HandlerFunc(func(context.Context, Event) error {
			t.Error("handler ran for an undecodable delivery")
			return nil
		}),
	}}})
	defer stop()

	if s := post(t, url+"/x", nil, []byte(`{}`)); s != http.StatusBadRequest {
		t.Errorf("undecodable delivery = %d, want 400", s)
	}
}

// A provider that supplies no delivery id cannot be deduped. Dropping the
// delivery instead would be worse: better handled twice than never.
func TestEmptyDeliveryIDSkipsDedup(t *testing.T) {
	handled := make(chan struct{}, 4)
	url, stop := startServer(t, Config{Endpoints: []Endpoint{{
		Path:    "/x",
		Auth:    AuthenticatorFunc(func(context.Context, *http.Request, []byte) error { return nil }),
		Decode:  func(*http.Request, []byte) (string, string, error) { return "e", "", nil },
		Handler: HandlerFunc(func(context.Context, Event) error { handled <- struct{}{}; return nil }),
	}}})
	defer stop()

	for range 2 {
		if s := post(t, url+"/x", nil, []byte(`{}`)); s != http.StatusAccepted {
			t.Fatalf("delivery = %d, want 202", s)
		}
		select {
		case <-handled:
		case <-time.After(5 * time.Second):
			t.Fatal("delivery with no id was deduped away")
		}
	}
}

func TestMaxBody(t *testing.T) {
	url, stop := startServer(t, Config{Endpoints: []Endpoint{{
		Path:    "/x",
		MaxBody: 16,
		Auth:    AuthenticatorFunc(func(context.Context, *http.Request, []byte) error { return nil }),
		Decode:  func(*http.Request, []byte) (string, string, error) { return "e", "", nil },
		Handler: HandlerFunc(func(context.Context, Event) error { return nil }),
	}}})
	defer stop()

	if s := post(t, url+"/x", nil, []byte(strings.Repeat("x", 17))); s != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body = %d, want 413", s)
	}
}
