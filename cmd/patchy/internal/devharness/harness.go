// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package devharness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/bitwise-media-group/patchy/internal/generic"
	"github.com/bitwise-media-group/patchy/internal/webhook"
	pkggeneric "github.com/bitwise-media-group/patchy/pkg/generic"
	"github.com/bitwise-media-group/patchy/pkg/source"
)

// maxEnrichmentMarkdown mirrors the context controller's cap: production
// truncates an enhancer's comment at this many bytes. The harness never
// truncates — it warns instead, which is more useful to the author.
const maxEnrichmentMarkdown = 16384

// Config configures a Harness.
type Config struct {
	// Addr is the listen address; ":0" picks an ephemeral port, reported by
	// the listening event.
	Addr string
	// Name is the integration name: the path segment, the source id, and
	// the Integration field of every outbound request.
	Name string
	// Secret is the shared HMAC secret, used in both directions.
	Secret []byte
	// MinSeverity drops findings below this level, exactly as the
	// Integration's spec.generic.source.minSeverity would; empty admits
	// everything.
	MinSeverity string
	// EnhanceURL is the author's enhancer endpoint; empty disables the leg.
	EnhanceURL string
	// EnhanceTimeout bounds each enhancer call; zero means the contract
	// default of 60s.
	EnhanceTimeout time.Duration
	// ResolveURL is the author's resolver endpoint; empty disables the leg.
	ResolveURL string
	// ResolveTimeout bounds each resolver call; zero means 60s.
	ResolveTimeout time.Duration
	// ResolveDelay pauses between a finding's enhance and resolve calls.
	ResolveDelay time.Duration
	// AutoResolve fires the resolver write-back for each ingested finding;
	// off, findings are only retained and the author resolves them with the
	// one-shot command.
	AutoResolve bool
	// Log receives server diagnostics — why a delivery 401'd, handler
	// failures. The wire deliberately gives senders no oracle, so this is
	// where the author looks. Nil discards.
	Log *slog.Logger
	// Events receives every pipeline event for rendering. Deliveries are
	// handled by a single worker, so calls never overlap. Nil discards.
	Events func(Event)
}

// Event is one observable step of the harness pipeline; it doubles as the
// NDJSON line for machine-readable output.
type Event struct {
	// Kind discriminates: listening, delivery, finding, enhance, resolve,
	// or error.
	Kind string `json:"kind"`
	// WebhookURL is the full inbound URL (listening events).
	WebhookURL string `json:"webhookUrl,omitempty"`
	// DeliveryID identifies the inbound delivery (delivery events).
	DeliveryID string `json:"deliveryId,omitempty"`
	// Delivered and Ingested count a delivery's findings before and after
	// the severity floor — a gap means silent min-severity drops, which is
	// contract behavior worth surfacing.
	Delivered int `json:"delivered,omitempty"`
	Ingested  int `json:"ingested,omitempty"`
	// Finding is the ingested finding (finding events).
	Finding *source.Finding `json:"finding,omitempty"`
	// EnhanceRequest/EnhanceResponse are the enhancer exchange (enhance
	// events); a nil response means 204 or an empty body.
	EnhanceRequest  *pkggeneric.EnhanceRequest  `json:"enhanceRequest,omitempty"`
	EnhanceResponse *pkggeneric.EnhanceResponse `json:"enhanceResponse,omitempty"`
	// ResolveRequest is the write-back sent (resolve events).
	ResolveRequest *pkggeneric.ResolveRequest `json:"resolveRequest,omitempty"`
	// Note carries emulation caveats and contract reminders.
	Note string `json:"note,omitempty"`
	// Err is the failure of this step, when it failed.
	Err string `json:"err,omitempty"`
}

// Summary counts what a run processed, for the shutdown line.
type Summary struct {
	Deliveries      int `json:"deliveries"`
	Findings        int `json:"findings"`
	EnhanceCalls    int `json:"enhanceCalls"`
	EnhanceFailures int `json:"enhanceFailures"`
	ResolveCalls    int `json:"resolveCalls"`
	ResolveFailures int `json:"resolveFailures"`
}

// Harness hosts the receiver and drives the outbound legs.
type Harness struct {
	cfg     Config
	enhance *generic.Client // nil when the leg is off
	resolve *generic.Client // nil when the leg is off

	mu       sync.Mutex
	findings []source.Finding
	sum      Summary
}

// New builds a Harness. Clients are built eagerly so a missing secret or a
// malformed URL fails here, before anything listens.
func New(cfg Config) (*Harness, error) {
	if cfg.Name == "" {
		return nil, errors.New("devharness: an integration name is required")
	}
	if len(cfg.Secret) == 0 {
		return nil, errors.New("devharness: a webhook secret is required")
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.Events == nil {
		cfg.Events = func(Event) {}
	}
	h := &Harness{cfg: cfg}
	var err error
	if cfg.EnhanceURL != "" {
		h.enhance, err = NewClient(cfg.EnhanceURL, cfg.Secret, cfg.EnhanceTimeout)
		if err != nil {
			return nil, err
		}
	}
	if cfg.ResolveURL != "" {
		h.resolve, err = NewClient(cfg.ResolveURL, cfg.Secret, cfg.ResolveTimeout)
		if err != nil {
			return nil, err
		}
	}
	return h, nil
}

// NewClient builds the signed outbound client for one leg or a one-shot
// invocation; the zero timeout means the contract default of 60s.
func NewClient(url string, secret []byte, timeout time.Duration) (*generic.Client, error) {
	return generic.NewClient(generic.ClientOptions{URL: url, Secret: secret, Timeout: timeout})
}

// Run listens, serves until ctx is cancelled, then drains gracefully. The
// listening event reports the bound address, so ":0" works.
func (h *Harness) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", h.cfg.Addr)
	if err != nil {
		return fmt.Errorf("devharness: listen on %s: %w", h.cfg.Addr, err)
	}
	// One worker keeps deliveries — and therefore events — strictly ordered;
	// a dev tool wants a readable transcript, not throughput.
	srv, err := webhook.NewServer(webhook.Config{
		Addr:      h.cfg.Addr,
		Workers:   1,
		Endpoints: []webhook.Endpoint{h.endpoint()},
	}, h.cfg.Log)
	if err != nil {
		_ = ln.Close()
		return fmt.Errorf("devharness: build server: %w", err)
	}
	h.cfg.Events(Event{
		Kind:       "listening",
		WebhookURL: "http://" + ln.Addr().String() + generic.PathFor(h.cfg.Name),
	})
	return srv.Serve(ctx, ln)
}

// Findings snapshots the retained findings, in ingest order.
func (h *Harness) Findings() []source.Finding {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]source.Finding, len(h.findings))
	copy(out, h.findings)
	return out
}

// Summary snapshots the run's counters.
func (h *Harness) Summary() Summary {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sum
}

// endpoint assembles the generic route exactly as the integration-controller
// does — same pattern, same authenticator, same decoder — with the secret
// lookup answered from configuration instead of an Integration resource.
func (h *Harness) endpoint() webhook.Endpoint {
	return webhook.Endpoint{
		Path: generic.PathPattern,
		Auth: &webhook.HMACAuthenticator{
			Header: pkggeneric.SignatureHeader,
			SecretsForRequest: func(_ context.Context, r *http.Request) [][]byte {
				if r.PathValue("name") == h.cfg.Name {
					return [][]byte{h.cfg.Secret}
				}
				return nil // unknown name fails closed — the same no-oracle 401 as production
			},
		},
		Decode:  decode,
		Handler: webhook.HandlerFunc(h.handle),
	}
}

// decode labels a generic delivery; it mirrors the integration-controller's
// unexported genericDecoder verbatim.
func decode(r *http.Request, body []byte) (eventType, deliveryID string, err error) {
	event, err := generic.Detect(body)
	if err != nil {
		return "", "", err
	}
	return event, generic.DeliveryID(r, body), nil
}

// handle ingests one authenticated delivery and drives the outbound legs for
// each finding. It mirrors the controller's handleGeneric/ingestAll flow with
// the in-memory store in place of Finding resources.
func (h *Harness) handle(ctx context.Context, e webhook.Event) error {
	name, ok := generic.NameFromPath(e.Path)
	if !ok {
		return fmt.Errorf("devharness: generic delivery on unparseable path %s", e.Path)
	}
	h.mu.Lock()
	h.sum.Deliveries++
	h.mu.Unlock()

	src := generic.NewSource(name, generic.Options{MinSeverity: h.cfg.MinSeverity})
	findings, err := src.Findings(ctx, e.Type, e.Payload)
	if err != nil {
		// The sender already got its 202: validation is asynchronous in
		// production too, so a rejected delivery only ever surfaces here.
		h.cfg.Events(Event{
			Kind:       "error",
			DeliveryID: e.DeliveryID,
			Note:       "the sender already received 202 — validation is asynchronous, exactly as in production",
			Err:        err.Error(),
		})
		return err
	}

	delivery := Event{Kind: "delivery", DeliveryID: e.DeliveryID, Delivered: delivered(e.Payload), Ingested: len(findings)}
	if delivery.Ingested < delivery.Delivered {
		delivery.Note = fmt.Sprintf(
			"%d finding(s) below minSeverity %q were dropped silently — contract behavior, not an error",
			delivery.Delivered-delivery.Ingested, h.cfg.MinSeverity)
	}
	h.cfg.Events(delivery)

	for i := range findings {
		f := findings[i]
		h.mu.Lock()
		h.findings = append(h.findings, f)
		h.sum.Findings++
		h.mu.Unlock()
		h.cfg.Events(Event{Kind: "finding", DeliveryID: e.DeliveryID, Finding: &f})

		if h.enhance != nil {
			ev := enhanceOne(ctx, h.enhance, name, f)
			h.mu.Lock()
			h.sum.EnhanceCalls++
			if ev.Err != "" {
				h.sum.EnhanceFailures++
			}
			h.mu.Unlock()
			h.cfg.Events(ev)
			// An enhancer failure never blocks the rest of the pipeline in
			// production (errors are warn-and-skip), so resolve still fires.
		}
		if h.resolve != nil && h.cfg.AutoResolve {
			if !sleep(ctx, h.cfg.ResolveDelay) {
				return ctx.Err()
			}
			ev := resolveOne(ctx, h.resolve, name, f)
			h.mu.Lock()
			h.sum.ResolveCalls++
			if ev.Err != "" {
				h.sum.ResolveFailures++
			}
			h.mu.Unlock()
			h.cfg.Events(ev)
		}
	}
	return nil
}

// delivered counts the findings a payload carried before validation and the
// severity floor, so the delivery event can surface silent drops.
func delivered(payload []byte) int {
	var p pkggeneric.Payload
	if err := json.Unmarshal(payload, &p); err != nil {
		return 0
	}
	return len(p.Findings)
}

// sleep waits d unless ctx ends first; it reports whether the wait completed.
func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
