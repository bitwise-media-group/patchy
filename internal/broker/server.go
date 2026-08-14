// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package broker

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"time"

	"k8s.io/client-go/kubernetes"
)

// Server is the broker engine: authenticated, audited reverse proxies for the
// configured upstream routes.
type Server struct {
	cfg    Config
	auth   *authenticator
	routes map[string]*route
	log    *slog.Logger
}

// New builds a Server. cs performs the TokenReviews; the broker needs no
// other Kubernetes access of any kind.
func New(cs kubernetes.Interface, cfg Config, log *slog.Logger) (*Server, error) {
	cfg = cfg.withDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &Server{cfg: cfg, auth: newAuthenticator(cs, cfg), routes: map[string]*route{}, log: log}
	for name, u := range cfg.Upstreams {
		s.routes[name] = newRoute(name, u, log)
	}
	return s, nil
}

// Handler is the proxy surface: one subtree per configured route, everything
// under it requiring a valid caller token — plus the (unauthenticated) probe
// endpoints, so controllers can check readiness over the same Service URL
// agent pods proxy through.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	for name, rt := range s.routes {
		prefix := "/" + name
		mux.Handle(prefix+"/", http.StripPrefix(prefix, s.handle(rt)))
	}
	s.mountHealth(mux)
	return mux
}

// handle authenticates, audits, buffers when the route signs payloads, and
// forwards.
func (s *Server) handle(rt *route) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id, err := s.auth.authenticate(r)
		if err != nil {
			writeAPIError(w, http.StatusUnauthorized, "authentication_error", err.Error())
			audit(s.log, r, "", rt.name, http.StatusUnauthorized, 0, time.Since(start))
			return
		}
		if rt.upstream.BufferBody {
			ok, err := bufferBody(r, s.cfg.MaxRequestBytes)
			if err != nil {
				writeAPIError(w, http.StatusBadRequest, "invalid_request_error", "egress broker: read request body")
				audit(s.log, r, id.pod, rt.name, http.StatusBadRequest, 0, time.Since(start))
				return
			}
			if !ok {
				writeAPIError(w, http.StatusRequestEntityTooLarge, "invalid_request_error",
					fmt.Sprintf("egress broker: request body exceeds %d bytes", s.cfg.MaxRequestBytes))
				audit(s.log, r, id.pod, rt.name, http.StatusRequestEntityTooLarge, 0, time.Since(start))
				return
			}
		}

		aw := &auditWriter{ResponseWriter: w}
		sw := newSSEWriter(aw, s.cfg.PingInterval)
		defer sw.stop()
		rt.proxy.ServeHTTP(sw, r)
		audit(s.log, r, id.pod, rt.name, aw.status, aw.bytes, time.Since(start))
	})
}

// HealthHandler serves the probe endpoints on the health listener: healthz
// is liveness, readyz additionally requires every configured route's
// credential source to be usable — which is what replaces controller-side
// Secret probing as "is the model credential present". Neither touches the
// API server: TokenReview being down must fail requests, not probes.
func (s *Server) HealthHandler() http.Handler {
	mux := http.NewServeMux()
	s.mountHealth(mux)
	return mux
}

// mountHealth registers the probe endpoints on a mux.
func (s *Server) mountHealth(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		for _, name := range slices.Sorted(maps.Keys(s.routes)) {
			rt := s.routes[name]
			if rt.upstream.Ready == nil {
				continue
			}
			if err := rt.upstream.Ready(ctx); err != nil {
				s.log.LogAttrs(ctx, slog.LevelWarn, "route not ready",
					slog.String("route", name), slog.Any("error", err))
				http.Error(w, fmt.Sprintf("route %s: credential source unusable", name),
					http.StatusServiceUnavailable)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
	})
}
