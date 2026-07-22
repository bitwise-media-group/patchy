// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

const scopeName = "github.com/bitwise-media-group/patchy/internal/webhook"

// maxBody matches GitHub's 25 MB webhook payload cap.
const maxBody = 25 << 20

// dedupTTL bounds how long a delivery GUID is treated as a duplicate.
// Redeliveries reuse the original GUID, so this must be long enough to
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
	// Type is the X-GitHub-Event header value, e.g. "code_scanning_alert".
	Type string
	// DeliveryID is the X-GitHub-Delivery GUID.
	DeliveryID string
	// Payload is the raw JSON body.
	Payload []byte
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
	// Secret is the shared webhook secret used to validate
	// X-Hub-Signature-256.
	Secret []byte
	// SecretsFor, when set, supersedes Secret: it returns the candidate
	// secrets for a delivery (e.g. the webhook secrets of every configured
	// Integration) and validation accepts a signature matching any of them.
	// Secrets are configuration objects, so the candidate set is tiny.
	SecretsFor func(ctx context.Context) [][]byte
	// Path is the webhook endpoint path. Default "/webhook".
	Path string
	// Workers is the handler pool size. Default 4.
	Workers int
	// QueueSize bounds the delivery queue; a full queue answers 503 so
	// GitHub redelivers. Default 64.
	QueueSize int
	// Ready optionally gates /readyz; nil means ready once serving.
	Ready func() bool
}

// Server is the webhook HTTP server.
type Server struct {
	cfg   Config
	log   *slog.Logger
	h     Handler
	queue chan Event
	seen  *dedup
}

// NewServer builds a Server; defaults are applied here so the zero Config
// fields need no ceremony at call sites.
func NewServer(cfg Config, log *slog.Logger, h Handler) *Server {
	if cfg.Path == "" {
		cfg.Path = "/webhook"
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
		h:     h,
		queue: make(chan Event, cfg.QueueSize),
		seen:  newDedup(1024, dedupTTL),
	}
}

// ResetDedup drops the delivery dedup window, so redeliveries of
// already-seen GUIDs are handled as new. Called when an Integration's
// reset or replay request is consumed — both make GitHub resend deliveries
// whose GUIDs the receiver has already recorded.
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
	mux.HandleFunc("POST "+s.cfg.Path, s.receive)
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
			for e := range s.queue {
				s.dispatch(workCtx, e)
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

// receive validates one delivery and enqueues it. GitHub gets its answer
// before any handling happens: 202 on accept, 401 on a bad signature, 503
// when the queue is full (GitHub retries).
func (s *Server) receive(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		s.count(r.Context(), "", "oversized")
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return
	}

	secrets := [][]byte{s.cfg.Secret}
	if s.cfg.SecretsFor != nil {
		secrets = s.cfg.SecretsFor(r.Context())
	}
	if !slices.ContainsFunc(secrets, func(secret []byte) bool {
		return validSignature(secret, body, r.Header.Get("X-Hub-Signature-256"))
	}) {
		s.count(r.Context(), "", "bad-signature")
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	event := Event{
		Type:       r.Header.Get("X-GitHub-Event"),
		DeliveryID: r.Header.Get("X-GitHub-Delivery"),
		Payload:    body,
	}
	if event.Type == "" {
		s.count(r.Context(), "", "missing-event")
		http.Error(w, "missing X-GitHub-Event", http.StatusBadRequest)
		return
	}
	if event.Type == "ping" {
		s.count(r.Context(), event.Type, "ping")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if event.DeliveryID != "" && !s.seen.add(event.DeliveryID) {
		s.count(r.Context(), event.Type, "duplicate")
		w.WriteHeader(http.StatusAccepted)
		return
	}

	select {
	case s.queue <- event:
		s.count(r.Context(), event.Type, "accepted")
		w.WriteHeader(http.StatusAccepted)
	default:
		// Roll back dedup so the redelivery is not mistaken for a duplicate.
		s.seen.remove(event.DeliveryID)
		s.count(r.Context(), event.Type, "queue-full")
		http.Error(w, "queue full, retry", http.StatusServiceUnavailable)
	}
}

func (s *Server) dispatch(ctx context.Context, e Event) {
	ctx, span := tracer().Start(ctx, "patchy.webhook.delivery",
		trace.WithAttributes(
			attribute.String("github.event", e.Type),
			attribute.String("github.delivery", e.DeliveryID)))
	defer span.End()

	if err := s.h.Handle(ctx, e); err != nil {
		span.SetStatus(codes.Error, err.Error())
		s.count(ctx, e.Type, "handler-error")
		s.log.LogAttrs(ctx, slog.LevelError, "webhook handler failed",
			slog.String("event", e.Type),
			slog.String("delivery", e.DeliveryID),
			slog.Any("error", err))
		return
	}
	s.count(ctx, e.Type, "handled")
}

func (s *Server) count(ctx context.Context, event, result string) {
	deliveries().Add(ctx, 1, metric.WithAttributes(
		attribute.String("event", event),
		attribute.String("result", result)))
}

// validSignature checks the X-Hub-Signature-256 header ("sha256=<hex>")
// against the HMAC of the raw body, in constant time.
func validSignature(secret, body []byte, header string) bool {
	if len(secret) == 0 || header == "" {
		return false
	}
	const prefix = "sha256="
	if len(header) <= len(prefix) || header[:len(prefix)] != prefix {
		return false
	}
	want, err := hex.DecodeString(header[len(prefix):])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), want)
}
