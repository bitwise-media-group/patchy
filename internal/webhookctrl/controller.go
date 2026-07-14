// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package webhookctrl

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/bitwise-media-group/patchy/internal/webhook"
)

const scopeName = "github.com/bitwise-media-group/patchy/internal/webhookctrl"

var forwards = sync.OnceValue(func() metric.Int64Counter {
	c, err := otel.Meter(scopeName).Int64Counter("patchy.webhookctrl.forwards",
		metric.WithDescription("webhook deliveries forwarded by target and result"))
	if err != nil {
		otel.Handle(err)
	}
	return c
})

// DefaultRoute is the Routes key that catches event types with no route of
// their own.
const DefaultRoute = "*"

// Config configures a Forwarder.
type Config struct {
	// Secret is the shared webhook HMAC secret. The forwarded
	// X-Hub-Signature-256 is recomputed from it — byte-identical to the one
	// GitHub sent — so the controllers keep validating every delivery.
	Secret []byte
	// Routes maps an X-GitHub-Event type to the webhook endpoints that
	// consume it, e.g. "code_scanning_alert" ->
	// http://patchy-source-controller:8080/webhook. The DefaultRoute ("*")
	// entry, when present, receives every event type no other key claims.
	Routes map[string][]string
	// Timeout bounds each per-target forward. Default 10s.
	Timeout time.Duration
	// Client optionally overrides the HTTP client (tests). Timeouts are
	// applied per request via context, not on the client.
	Client *http.Client
}

// Forwarder routes one validated delivery to the targets that consume its
// event type. It implements webhook.Handler, so it plugs straight into
// webhook.NewServer.
type Forwarder struct {
	cfg Config
	log *slog.Logger
}

// New builds a Forwarder; defaults are applied here so the zero Config
// fields need no ceremony at call sites.
func New(cfg Config, log *slog.Logger) *Forwarder {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{}
	}
	return &Forwarder{cfg: cfg, log: log}
}

// Handle forwards the delivery to its event type's targets concurrently and
// reports the targets that failed. A partial failure still delivers to the
// healthy targets; the failed controller's reconcile loop is the retry
// mechanism. An event type with no route (and no DefaultRoute) is dropped
// without error — GitHub only sends subscribed events, so this is a routing
// config gap, not a delivery failure worth retrying.
func (f *Forwarder) Handle(ctx context.Context, e webhook.Event) error {
	targets := f.cfg.Routes[e.Type]
	if len(targets) == 0 {
		targets = f.cfg.Routes[DefaultRoute]
	}
	if len(targets) == 0 {
		f.count(ctx, "", "unrouted")
		f.log.LogAttrs(ctx, slog.LevelWarn, "no route for event type",
			slog.String("event", e.Type),
			slog.String("delivery", e.DeliveryID))
		return nil
	}

	sig := signature(f.cfg.Secret, e.Payload)
	errs := make([]error, len(targets))
	var wg sync.WaitGroup
	for i, target := range targets {
		wg.Go(func() {
			errs[i] = f.forward(ctx, target, e, sig)
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}

func (f *Forwarder) forward(ctx context.Context, target string, e webhook.Event, sig string) error {
	ctx, cancel := context.WithTimeout(ctx, f.cfg.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(e.Payload))
	if err != nil {
		f.count(ctx, target, "error")
		return fmt.Errorf("forward %s: %w", target, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", e.Type)
	req.Header.Set("X-GitHub-Delivery", e.DeliveryID)
	req.Header.Set("X-Hub-Signature-256", sig)

	resp, err := f.cfg.Client.Do(req)
	if err != nil {
		f.count(ctx, target, "error")
		return fmt.Errorf("forward %s: %w", target, err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		f.count(ctx, target, "rejected")
		return fmt.Errorf("forward %s: status %d", target, resp.StatusCode)
	}
	f.count(ctx, target, "forwarded")
	return nil
}

func (f *Forwarder) count(ctx context.Context, target, result string) {
	forwards().Add(ctx, 1, metric.WithAttributes(
		attribute.String("target", target),
		attribute.String("result", result)))
}

// signature renders the X-Hub-Signature-256 value for body.
func signature(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
