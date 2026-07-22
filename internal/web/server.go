// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/bitwise-media-group/patchy/internal/web/auth"
	"github.com/bitwise-media-group/patchy/internal/web/authz"
)

// maxBodyBytes caps action POST bodies; the endpoints take no payload, so
// anything past a token amount is noise.
const maxBodyBytes = 1 << 20

// Granter resolves what an identity may do; internal/web/authz provides the
// SubjectAccessReview implementation and the mode-none bypass.
type Granter interface {
	Grants(ctx context.Context, id auth.Identity) (authz.Grants, error)
}

// Server is the status-server backend: the public rollups projection, the
// authenticated findings projection, the three actions, the SSE change
// signal, and the embedded SPA.
type Server struct {
	client    client.Client
	namespace string
	auth      auth.Authenticator
	granter   Granter
	log       *slog.Logger
	broker    *broker
	now       func() time.Time
	// debounce overrides the watch coalescing window (tests).
	debounce time.Duration
}

// NewServer builds the backend over the manager's cached client. log may be
// nil.
func NewServer(c client.Client, namespace string, a auth.Authenticator, g Granter, log *slog.Logger) *Server {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Server{
		client:    c,
		namespace: namespace,
		auth:      a,
		granter:   g,
		log:       log,
		broker:    newBroker(),
		now:       time.Now,
	}
}

// Handler builds the HTTP surface: the split public/authenticated API, the
// SSE stream, the sign-in routes (when the auth mode has any), and the
// embedded SPA with client-side-routing fallback.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/findings", s.handleFindings)
	mux.HandleFunc("GET /api/rollups", s.handleRollups)
	mux.HandleFunc("POST /api/findings/{name}/actions/{verb}", s.handleAction)
	mux.HandleFunc("POST /api/admin/{verb}", s.handleAdmin)
	mux.HandleFunc("GET /events", s.handleEvents)
	s.auth.Register(mux)
	mux.Handle("/", s.staticHandler())
	return s.middleware(mux)
}

// middleware applies the security envelope to every response: conservative
// browser headers, no caching on the data surface, a body cap, and a
// same-origin check on mutations (defense in depth on top of SameSite=Lax
// cookies).
func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/events" {
			h.Set("Cache-Control", "no-store")
		}
		if r.Method == http.MethodPost {
			if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
				http.Error(w, "cross-origin request rejected", http.StatusForbidden)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
}

// handleFindings serves the full dataset to an authenticated identity whose
// RBAC grants viewing; rollups-only readers use /api/rollups.
func (s *Server) handleFindings(w http.ResponseWriter, r *http.Request) {
	id, err := s.auth.Identify(w, r)
	if err != nil {
		s.log.LogAttrs(r.Context(), slog.LevelError, "identify failed", slog.Any("error", err))
		http.Error(w, "authentication failed", http.StatusInternalServerError)
		return
	}
	if id == nil {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	grants, err := s.granter.Grants(r.Context(), *id)
	if err != nil {
		s.log.LogAttrs(r.Context(), slog.LevelError, "grants failed", slog.Any("error", err))
		http.Error(w, "authorization failed", http.StatusInternalServerError)
		return
	}
	if !grants.View {
		http.Error(w, fmt.Sprintf("Permission denied. User %q may not view findings in namespace %q.",
			id.Display(), s.namespace), http.StatusForbidden)
		return
	}
	user := &User{Name: id.Display(), LoggedIn: id.Session, AdminActions: grants.Admin}
	ds, err := s.buildDataset(r.Context(), true, grants.Verbs, user)
	if err != nil {
		s.log.LogAttrs(r.Context(), slog.LevelError, "build dataset", slog.Any("error", err))
		http.Error(w, "failed to load findings", http.StatusInternalServerError)
		return
	}
	writeJSON(w, ds)
}

// handleRollups serves the always-public statistics projection: the same
// dataset shape with no findings and no user.
func (s *Server) handleRollups(w http.ResponseWriter, r *http.Request) {
	ds, err := s.buildDataset(r.Context(), false, nil, nil)
	if err != nil {
		s.log.LogAttrs(r.Context(), slog.LevelError, "build rollups", slog.Any("error", err))
		http.Error(w, "failed to load rollups", http.StatusInternalServerError)
		return
	}
	writeJSON(w, ds)
}

// staticHandler serves the embedded SPA. Unknown paths fall back to the
// shell so the client-side router works; without the bundle (a bare
// `go build`) a stub page points at the tagged build.
func (s *Server) staticHandler() http.Handler {
	assets, ok := uiAssets()
	if !ok {
		return http.HandlerFunc(stubPage)
	}
	files := http.FileServer(http.FS(assets))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := strings.TrimPrefix(r.URL.Path, "/")
		if clean == "" {
			clean = "index.html"
		}
		if _, err := fs.Stat(assets, clean); err != nil {
			r.URL.Path = "/"
		}
		files.ServeHTTP(w, r)
	})
}

// stubPage is served when the SPA bundle was not compiled in.
func stubPage(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8">` +
		`<title>patchy status</title>` +
		`<body style="font-family:system-ui;max-width:40rem;margin:4rem auto;padding:0 1rem">` +
		`<h1>Status page not bundled</h1>` +
		`<p>This build of <code>status-server</code> was compiled without the status page assets. ` +
		`Build with <code>make build</code> (which builds the UI and compiles with ` +
		`<code>-tags withui</code>), or use a release image.</p>`))
}

// writeJSON encodes v as the response body.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}
