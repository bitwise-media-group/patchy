// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package webhook

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

const scopeName = "github.com/bitwise-media-group/patchy/internal/webhook"

// DefaultMaxBody matches GitHub's 25 MB webhook payload cap, comfortably above
// a Pub/Sub push (10 MB) too.
const DefaultMaxBody = 25 << 20

// dedupTTL bounds how long a delivery ID is treated as a duplicate.
// Redeliveries reuse the original ID, so this must be long enough to
// absorb a duplicate burst yet short enough that a deliberate redelivery
// (the GitHub UI, or the Integration's sweep/replay) is handled again.
const dedupTTL = 5 * time.Minute

var tracer = sync.OnceValue(func() trace.Tracer {
	return otel.Tracer(scopeName)
})

var deliveries = sync.OnceValue(func() metric.Int64Counter {
	c, err := otel.Meter(scopeName).Int64Counter("patchy.webhook.deliveries",
		metric.WithDescription("webhook deliveries by event type and result"))
	if err != nil {
		otel.Handle(err)
	}
	return c
})

// Event is one validated webhook delivery.
type Event struct {
	// Type is the provider's event discriminator, e.g. "code_scanning_alert".
	Type string
	// DeliveryID uniquely identifies the delivery, for deduplication.
	DeliveryID string
	// Payload is the raw JSON body.
	Payload []byte
	// Path is the request path the delivery arrived on, so a handler serving
	// more than one route can tell them apart. It equals the endpoint's
	// pattern for exact routes; for a wildcard route it is the concrete path,
	// carrying the matched segments (e.g. the integration name).
	Path string
}

// Handler consumes validated deliveries. Handle runs on a worker goroutine;
// returning an error records the failure but is not retried here — the
// reconcile loop is the retry mechanism.
type Handler interface {
	Handle(ctx context.Context, e Event) error
}

// HandlerFunc adapts a function to Handler.
type HandlerFunc func(ctx context.Context, e Event) error

// Handle implements Handler.
func (f HandlerFunc) Handle(ctx context.Context, e Event) error { return f(ctx, e) }

// Config configures a Server.
type Config struct {
	// Addr is the listen address, e.g. ":8080".
	Addr string
	// Endpoints are the provider routes to serve. At least one is required.
	Endpoints []Endpoint
	// Workers is the handler pool size. Default 4.
	Workers int
	// QueueSize bounds the delivery queue; a full queue answers 503 so the
	// provider redelivers. Default 64.
	QueueSize int
	// Ready optionally gates /readyz; nil means ready once serving.
	Ready func() bool
}

// Server is the webhook HTTP server. One listener serves every provider:
// patchy exposes a single internet-facing port, and the endpoints differ only
// in how a delivery is authenticated and labelled.
type Server struct {
	cfg   Config
	log   *slog.Logger
	queue chan queued
	seen  *dedup
}

// queued is a delivery waiting for a worker, with the handler that owns it.
type queued struct {
	event   Event
	handler Handler
}

// NewServer builds a Server; defaults are applied here so the zero Config
// fields need no ceremony at call sites. It fails rather than serving an
// endpoint that cannot authenticate or label its deliveries — an unauthicated
// internet-facing route is not something to discover at runtime.
func NewServer(cfg Config, log *slog.Logger) (*Server, error) {
	if len(cfg.Endpoints) == 0 {
		return nil, errors.New("webhook: no endpoints configured")
	}
	seen := make(map[string]bool, len(cfg.Endpoints))
	for i, e := range cfg.Endpoints {
		switch {
		case e.Path == "":
			return nil, fmt.Errorf("webhook: endpoint %d has no path", i)
		case e.Auth == nil:
			return nil, fmt.Errorf("webhook: endpoint %s has no authenticator", e.Path)
		case e.Decode == nil:
			return nil, fmt.Errorf("webhook: endpoint %s has no decoder", e.Path)
		case e.Handler == nil:
			return nil, fmt.Errorf("webhook: endpoint %s has no handler", e.Path)
		case seen[e.Path]:
			return nil, fmt.Errorf("webhook: endpoint %s is declared twice", e.Path)
		}
		seen[e.Path] = true
	}
	if cfg.Workers <= 0 {
		cfg.Workers = 4
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 64
	}
	return &Server{
		cfg:   cfg,
		log:   log,
		queue: make(chan queued, cfg.QueueSize),
		seen:  newDedup(1024, dedupTTL),
	}, nil
}

// ResetDedup drops the delivery dedup window, so redeliveries of
// already-seen IDs are handled as new. Called when an Integration's
// reset or replay request is consumed — both make the provider resend
// deliveries whose IDs the receiver has already recorded.
func (s *Server) ResetDedup() { s.seen.reset() }

// Run serves until ctx is cancelled, then drains: the listener shuts down
// gracefully, queued deliveries finish, and the worker pool exits.
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return err
	}
	return s.serve(ctx, ln)
}

// serve runs the accept loop on ln; split from Run so tests can inject a
// listener on an ephemeral port.
func (s *Server) serve(ctx context.Context, ln net.Listener) error {
	mux := http.NewServeMux()
	for _, e := range s.cfg.Endpoints {
		mux.HandleFunc("POST "+e.Path, s.receiver(e))
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if s.cfg.Ready != nil && !s.cfg.Ready() {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Workers consume until the queue closes; the queue closes only after
	// the listener has stopped accepting, so no delivery is dropped between
	// a 202 and its handling.
	var workers sync.WaitGroup
	workCtx, stopWork := context.WithCancel(context.WithoutCancel(ctx))
	defer stopWork()
	for range s.cfg.Workers {
		workers.Go(func() {
			for q := range s.queue {
				s.dispatch(workCtx, q)
			}
		})
	}

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

	select {
	case err := <-errc:
		close(s.queue)
		workers.Wait()
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	err := srv.Shutdown(shutdownCtx)
	close(s.queue)
	workers.Wait()
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return ctx.Err()
}

// receiver builds the HTTP handler for one endpoint. The provider gets its
// answer before any handling happens: 202 on accept, 401 on failed
// authentication, 503 when the queue is full (providers retry).
func (s *Server) receiver(e Endpoint) http.HandlerFunc {
	maxBody := e.MaxBody
	if maxBody <= 0 {
		maxBody = DefaultMaxBody
	}
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		body, err := io.ReadAll(io.LimitReader(r.Body, int64(maxBody)+1))
		if err != nil || len(body) > maxBody {
			s.count(ctx, e.Path, "", "oversized")
			http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
			return
		}

		if err := e.Auth.Authenticate(ctx, r, body); err != nil {
			s.count(ctx, e.Path, "", "unauthenticated")
			s.log.LogAttrs(ctx, slog.LevelWarn, "webhook delivery rejected",
				slog.String("path", e.Path), slog.Any("error", err))
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		eventType, deliveryID, err := e.Decode(r, body)
		if err != nil {
			s.count(ctx, e.Path, "", "undecodable")
			http.Error(w, "malformed delivery", http.StatusBadRequest)
			return
		}
		if eventType == "" {
			s.count(ctx, e.Path, "", "missing-event")
			http.Error(w, "missing event type", http.StatusBadRequest)
			return
		}
		if eventType == "ping" {
			s.count(ctx, e.Path, eventType, "ping")
			w.WriteHeader(http.StatusNoContent)
			return
		}

		// Dedup keys include the request path: two providers' id spaces are
		// unrelated — as are two integrations' behind one wildcard route —
		// and a collision between them would silently drop a delivery.
		key := r.URL.Path + "|" + deliveryID
		if deliveryID != "" && !s.seen.add(key) {
			s.count(ctx, e.Path, eventType, "duplicate")
			w.WriteHeader(http.StatusAccepted)
			return
		}

		// Metrics stay on the endpoint pattern (bounded cardinality); the
		// event carries the concrete request path.
		event := Event{Type: eventType, DeliveryID: deliveryID, Payload: body, Path: r.URL.Path}
		select {
		case s.queue <- queued{event: event, handler: e.Handler}:
			s.count(ctx, e.Path, eventType, "accepted")
			w.WriteHeader(http.StatusAccepted)
		default:
			// Roll back dedup so the redelivery is not mistaken for a duplicate.
			s.seen.remove(key)
			s.count(ctx, e.Path, eventType, "queue-full")
			http.Error(w, "queue full, retry", http.StatusServiceUnavailable)
		}
	}
}

func (s *Server) dispatch(ctx context.Context, q queued) {
	ctx, span := tracer().Start(ctx, "patchy.webhook.delivery",
		trace.WithAttributes(
			attribute.String("webhook.path", q.event.Path),
			attribute.String("webhook.event", q.event.Type),
			attribute.String("webhook.delivery", q.event.DeliveryID)))
	defer span.End()

	if err := q.handler.Handle(ctx, q.event); err != nil {
		span.SetStatus(codes.Error, err.Error())
		s.count(ctx, q.event.Path, q.event.Type, "handler-error")
		s.log.LogAttrs(ctx, slog.LevelError, "webhook handler failed",
			slog.String("path", q.event.Path),
			slog.String("event", q.event.Type),
			slog.String("delivery", q.event.DeliveryID),
			slog.Any("error", err))
		return
	}
	s.count(ctx, q.event.Path, q.event.Type, "handled")
}

func (s *Server) count(ctx context.Context, path, event, result string) {
	deliveries().Add(ctx, 1, metric.WithAttributes(
		attribute.String("path", path),
		attribute.String("event", event),
		attribute.String("result", result)))
}
