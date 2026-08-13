// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package enhancers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"golang.org/x/sync/semaphore"

	"github.com/bitwise-media-group/patchy/pkg/enhance"
	pkggeneric "github.com/bitwise-media-group/patchy/pkg/generic"
	"github.com/bitwise-media-group/patchy/pkg/source"
)

// genericHarness wires a DynamicGeneric to canned configs and a recording Do.
// The fan-out runs endpoints concurrently, so the recording is mutexed and
// assertions must not assume call order.
type genericHarness struct {
	cfgs    []GenericConfig
	cfgErr  error
	mu      sync.Mutex
	calls   []pkggeneric.EnhanceRequest
	respond func(cfg GenericConfig) (*pkggeneric.EnhanceResponse, error)
}

func (h *genericHarness) enhancer() *DynamicGeneric {
	return &DynamicGeneric{
		Configs: func(context.Context) ([]GenericConfig, error) { return h.cfgs, h.cfgErr },
		Do: func(_ context.Context, cfg GenericConfig, req pkggeneric.EnhanceRequest) (*pkggeneric.EnhanceResponse, error) {
			h.mu.Lock()
			h.calls = append(h.calls, req)
			h.mu.Unlock()
			return h.respond(cfg)
		},
	}
}

// callFor returns the recorded request for one integration.
func (h *genericHarness) callFor(t *testing.T, name string) pkggeneric.EnhanceRequest {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, req := range h.calls {
		if req.Integration == name {
			return req
		}
	}
	t.Fatalf("no call recorded for %q (calls: %+v)", name, h.calls)
	return pkggeneric.EnhanceRequest{}
}

func TestDynamicGenericFansOut(t *testing.T) {
	h := &genericHarness{
		// Deliberately unsorted: precedence must be name order, not list order.
		cfgs: []GenericConfig{{Name: "warehouse"}, {Name: "cmdb"}},
		respond: func(cfg GenericConfig) (*pkggeneric.EnhanceResponse, error) {
			return &pkggeneric.EnhanceResponse{
				CommentMarkdown: "from " + cfg.Name,
				Attributes:      map[string]string{"origin": cfg.Name},
			}, nil
		},
	}
	issue := enhance.Issue{
		Repo:  source.Repo{Owner: "acme", Name: "orders"},
		Title: "Reflected XSS",
	}
	got, err := h.enhancer().EnhanceAll(t.Context(), issue)
	if err != nil {
		t.Fatalf("EnhanceAll() = %v, want nil", err)
	}
	if len(got) != 2 || got[0].ID != "cmdb" || got[1].ID != "warehouse" {
		t.Fatalf("enrichments = %+v, want both, sorted by name", got)
	}
	if got[0].Enrichment.CommentMarkdown != "from cmdb" {
		t.Errorf("cmdb enrichment = %+v, want its own contribution", got[0].Enrichment)
	}
	if len(h.calls) != 2 {
		t.Fatalf("endpoints called %d times, want 2", len(h.calls))
	}
	req := h.callFor(t, "cmdb")
	if req.Version != pkggeneric.Version || req.Integration != "cmdb" {
		t.Errorf("request = %+v, want version and integration stamped", req)
	}
	if req.Issue.Title != "Reflected XSS" || req.Issue.Repo == nil || req.Issue.Repo.Owner != "acme" {
		t.Errorf("issue view = %+v, want the finding mapped onto the wire", req.Issue)
	}
}

// No enabled generic integration is "not ours": (nil, nil), the chain moves
// on.
func TestDynamicGenericStandsAsideWhenOff(t *testing.T) {
	h := &genericHarness{respond: func(GenericConfig) (*pkggeneric.EnhanceResponse, error) {
		t.Fatal("no endpoint should be called")
		return nil, nil
	}}
	got, err := h.enhancer().EnhanceAll(t.Context(), enhance.Issue{})
	if got != nil || err != nil {
		t.Errorf("EnhanceAll() = (%+v, %v), want (nil, nil)", got, err)
	}
}

// One broken endpoint neither discards the others' work nor hides: partial
// results come back beside a joined error naming the failure.
func TestDynamicGenericPartialFailure(t *testing.T) {
	h := &genericHarness{
		cfgs: []GenericConfig{{Name: "cmdb"}, {Name: "warehouse"}},
		respond: func(cfg GenericConfig) (*pkggeneric.EnhanceResponse, error) {
			if cfg.Name == "cmdb" {
				return nil, errors.New("connection refused")
			}
			return &pkggeneric.EnhanceResponse{Owners: []string{"alice"}}, nil
		},
	}
	got, err := h.enhancer().EnhanceAll(t.Context(), enhance.Issue{})
	if err == nil || !strings.Contains(err.Error(), "cmdb") {
		t.Errorf("error = %v, want the broken integration named", err)
	}
	if len(got) != 1 || got[0].ID != "warehouse" {
		t.Errorf("enrichments = %+v, want the surviving endpoint's", got)
	}
}

// A 204 (nil response) is a valid "nothing to contribute", not an enrichment.
func TestDynamicGenericNothingToContribute(t *testing.T) {
	h := &genericHarness{
		cfgs:    []GenericConfig{{Name: "cmdb"}},
		respond: func(GenericConfig) (*pkggeneric.EnhanceResponse, error) { return nil, nil },
	}
	got, err := h.enhancer().EnhanceAll(t.Context(), enhance.Issue{})
	if len(got) != 0 || err != nil {
		t.Errorf("EnhanceAll() = (%+v, %v), want no enrichments and no error", got, err)
	}
}

// The semaphore is the global outbound bound: with Limit=2 over eight
// endpoints, no more than two calls may ever be in flight together.
func TestDynamicGenericBoundsOutbound(t *testing.T) {
	var inFlight, peak atomic.Int64
	release := make(chan struct{})
	h := &genericHarness{
		respond: func(GenericConfig) (*pkggeneric.EnhanceResponse, error) {
			cur := inFlight.Add(1)
			for {
				old := peak.Load()
				if cur <= old || peak.CompareAndSwap(old, cur) {
					break
				}
			}
			<-release
			inFlight.Add(-1)
			return &pkggeneric.EnhanceResponse{CommentMarkdown: "ok"}, nil
		},
	}
	for i := range 8 {
		h.cfgs = append(h.cfgs, GenericConfig{Name: fmt.Sprintf("endpoint-%d", i)})
	}
	e := h.enhancer()
	e.Limit = semaphore.NewWeighted(2)

	done := make(chan struct{})
	var got []AttributedEnrichment
	var err error
	go func() {
		got, err = e.EnhanceAll(context.Background(), enhance.Issue{})
		close(done)
	}()
	// Unblock the calls one at a time; the semaphore admits the next as each
	// completes.
	for range 8 {
		release <- struct{}{}
	}
	<-done
	if err != nil {
		t.Fatalf("EnhanceAll() = %v, want nil", err)
	}
	if len(got) != 8 {
		t.Fatalf("enrichments = %d, want all 8", len(got))
	}
	if p := peak.Load(); p > 2 {
		t.Errorf("peak in-flight calls = %d, want <= the semaphore's 2", p)
	}
}

// Results stay name-sorted however the concurrent calls complete.
func TestDynamicGenericDeterministicOrder(t *testing.T) {
	h := &genericHarness{
		cfgs: []GenericConfig{{Name: "zeta"}, {Name: "alpha"}, {Name: "mid"}},
		respond: func(cfg GenericConfig) (*pkggeneric.EnhanceResponse, error) {
			return &pkggeneric.EnhanceResponse{CommentMarkdown: cfg.Name}, nil
		},
	}
	for range 20 {
		got, err := h.enhancer().EnhanceAll(t.Context(), enhance.Issue{})
		if err != nil {
			t.Fatalf("EnhanceAll() = %v, want nil", err)
		}
		if len(got) != 3 || got[0].ID != "alpha" || got[1].ID != "mid" || got[2].ID != "zeta" {
			t.Fatalf("order = %v, want name-sorted regardless of completion order", ids(got))
		}
	}
}

func ids(enrs []AttributedEnrichment) []string {
	out := make([]string, 0, len(enrs))
	for _, e := range enrs {
		out = append(out, e.ID)
	}
	return out
}

func TestDynamicGenericConfigError(t *testing.T) {
	h := &genericHarness{cfgErr: errors.New("list failed")}
	if _, err := h.enhancer().EnhanceAll(t.Context(), enhance.Issue{}); err == nil {
		t.Error("EnhanceAll(config error) = nil error, want one so the finding holds")
	}
}

// The default Do path drives the real signed client end to end: each
// endpoint sees a request signed with its own secret and its response maps
// onto the seam type.
func TestDynamicGenericDefaultClient(t *testing.T) {
	var gotSig string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig = r.Header.Get(pkggeneric.SignatureHeader)
		_, _ = fmt.Fprint(w, `{"repository":{"provider":"github","url":"https://github.com/acme/infra"}}`)
	}))
	t.Cleanup(srv.Close)

	d := &DynamicGeneric{Configs: func(context.Context) ([]GenericConfig, error) {
		return []GenericConfig{{Name: "cmdb", URL: srv.URL, Secret: []byte("shared")}}, nil
	}}
	got, err := d.EnhanceAll(t.Context(), enhance.Issue{Title: "t"})
	if err != nil {
		t.Fatalf("EnhanceAll() = %v, want nil", err)
	}
	if len(got) != 1 || got[0].Enrichment.Repository == nil ||
		got[0].Enrichment.Repository.URL != "https://github.com/acme/infra" {
		t.Errorf("enrichments = %+v, want the resolved repository", got)
	}
	if !strings.HasPrefix(gotSig, "sha256=") {
		t.Errorf("signature header = %q, want an HMAC signature", gotSig)
	}

	// A config whose Secret could not be read fails its own call with
	// attribution instead of being silently dropped from the fan-out.
	d = &DynamicGeneric{Configs: func(context.Context) ([]GenericConfig, error) {
		return []GenericConfig{{Name: "cmdb", URL: srv.URL}}, nil
	}}
	if _, err := d.EnhanceAll(t.Context(), enhance.Issue{}); err == nil ||
		!strings.Contains(err.Error(), "cmdb") {
		t.Errorf("EnhanceAll(secretless config) = %v, want an attributed error", err)
	}
}
