// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package devharness

import (
	"crypto/hmac"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"

	"github.com/bitwise-media-group/patchy/internal/generic"
	pkggeneric "github.com/bitwise-media-group/patchy/pkg/generic"
)

// authorProcess is the external process under test's stand-in — the mirror
// image of the harness, modeled on the e2e suite's externalProcess: it
// verifies outbound signatures and records what patchy sent.
type authorProcess struct {
	secret []byte

	mu             sync.Mutex
	enhanceStatus  int // 0 means 200 with enhanceReply
	enhanceReply   *pkggeneric.EnhanceResponse
	resolveStatus  int // 0 means 200
	enhanceReqs    []pkggeneric.EnhanceRequest
	resolveReqs    []pkggeneric.ResolveRequest
	badSignatures  int
	unparsedBodies int
}

// serve starts the endpoint; the same server answers both legs by path.
func (a *authorProcess) serve() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /enhance", a.handleEnhance)
	mux.HandleFunc("POST /resolve", a.handleResolve)
	return httptest.NewServer(mux)
}

func (a *authorProcess) read(r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, false
	}
	sig := r.Header.Get(pkggeneric.SignatureHeader)
	if !hmac.Equal([]byte(sig), []byte(generic.Sign(a.secret, body))) {
		a.mu.Lock()
		a.badSignatures++
		a.mu.Unlock()
		return nil, false
	}
	return body, true
}

func (a *authorProcess) handleEnhance(w http.ResponseWriter, r *http.Request) {
	body, ok := a.read(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var req pkggeneric.EnhanceRequest
	if err := json.Unmarshal(body, &req); err != nil {
		a.mu.Lock()
		a.unparsedBodies++
		a.mu.Unlock()
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	a.mu.Lock()
	a.enhanceReqs = append(a.enhanceReqs, req)
	status, reply := a.enhanceStatus, a.enhanceReply
	a.mu.Unlock()
	if status != 0 {
		w.WriteHeader(status)
		return
	}
	if reply == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(reply)
}

func (a *authorProcess) handleResolve(w http.ResponseWriter, r *http.Request) {
	body, ok := a.read(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var req pkggeneric.ResolveRequest
	if err := json.Unmarshal(body, &req); err != nil {
		a.mu.Lock()
		a.unparsedBodies++
		a.mu.Unlock()
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	a.mu.Lock()
	a.resolveReqs = append(a.resolveReqs, req)
	status := a.resolveStatus
	a.mu.Unlock()
	if status != 0 {
		w.WriteHeader(status)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// snapshot copies the recorded traffic.
func (a *authorProcess) snapshot() ([]pkggeneric.EnhanceRequest, []pkggeneric.ResolveRequest) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]pkggeneric.EnhanceRequest(nil), a.enhanceReqs...),
		append([]pkggeneric.ResolveRequest(nil), a.resolveReqs...)
}
