// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package evalapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/bitwise-media-group/patchy/internal/web/auth"
	"github.com/bitwise-media-group/patchy/pkg/evaluation"
)

// Verbs the API access-reviews on the evaluations resource.
const (
	VerbCreate = "create"
	VerbGet    = "get"
	VerbDelete = "delete"
)

// Granter answers whether an identity may verb evaluations; satisfied by
// authz.ResourceReviewer.
type Granter interface {
	Allowed(ctx context.Context, id auth.Identity, verb string) (bool, error)
}

// FullAccess grants everything — the mode-none bypass.
type FullAccess struct{}

// Allowed always says yes.
func (FullAccess) Allowed(context.Context, auth.Identity, string) (bool, error) { return true, nil }

// WorkspaceClient is the blob cache surface (source-controller's internal
// endpoint); satisfied by *artifact.Client.
type WorkspaceClient interface {
	Stat(ctx context.Context, digest string) (bool, error)
	Put(ctx context.Context, digest string, r io.Reader, size int64) error
}

// Limits bound the API's request surface.
type Limits struct {
	// MaxUnits per submission (default 200, and the CRD caps there too).
	MaxUnits int
	// MaxSubmissionBytes caps a POST body.
	MaxSubmissionBytes int64
	// MaxWorkspaceBytes caps a workspace upload.
	MaxWorkspaceBytes int64
	// MaxEvaluationBytes caps the marshalled Evaluation object (etcd's cap
	// with headroom).
	MaxEvaluationBytes int
}

func (l Limits) withDefaults() Limits {
	if l.MaxUnits <= 0 {
		l.MaxUnits = 200
	}
	if l.MaxSubmissionBytes <= 0 {
		l.MaxSubmissionBytes = 8 << 20
	}
	if l.MaxWorkspaceBytes <= 0 {
		l.MaxWorkspaceBytes = 64 << 20
	}
	if l.MaxEvaluationBytes <= 0 {
		l.MaxEvaluationBytes = 1 << 20
	}
	return l
}

// Server is the evaluation API.
type Server struct {
	client     client.Client
	namespace  string
	authn      Authenticator
	authInfo   evaluation.AuthInfo
	granter    Granter
	workspaces WorkspaceClient
	limits     Limits
	broker     *broker
	now        func() time.Time
	log        *slog.Logger
}

// NewServer builds the API server.
func NewServer(c client.Client, namespace string, authn Authenticator, info evaluation.AuthInfo,
	granter Granter, workspaces WorkspaceClient, limits Limits, log *slog.Logger) *Server {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Server{
		client:     c,
		namespace:  namespace,
		authn:      authn,
		authInfo:   info,
		granter:    granter,
		workspaces: workspaces,
		limits:     limits.withDefaults(),
		broker:     newBroker(),
		now:        time.Now,
		log:        log,
	}
}

// Handler is the API mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/auth/info", s.handleAuthInfo)
	mux.HandleFunc("HEAD /api/v1/workspaces/{digest}", s.handleWorkspaceHead)
	mux.HandleFunc("PUT /api/v1/workspaces/{digest}", s.handleWorkspacePut)
	mux.HandleFunc("POST /api/v1/evaluations", s.handleSubmit)
	mux.HandleFunc("GET /api/v1/evaluations/{name}", s.handleSnapshot)
	mux.HandleFunc("GET /api/v1/evaluations/{name}/events", s.handleEvents)
	mux.HandleFunc("DELETE /api/v1/evaluations/{name}", s.handleCancel)
	return s.middleware(mux)
}

// middleware sets the response hygiene headers.
func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// handleAuthInfo is the one unauthenticated route: what a login flow needs.
func (s *Server) handleAuthInfo(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.authInfo)
}

// authorize authenticates the request and access-reviews the verb; it has
// written the response when ok is false.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request, verb string) (auth.Identity, bool) {
	id, err := s.authn.Identify(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid credential")
		return auth.Identity{}, false
	}
	if id == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return auth.Identity{}, false
	}
	allowed, err := s.granter.Allowed(r.Context(), *id, verb)
	if err != nil {
		s.log.LogAttrs(r.Context(), slog.LevelError, "access review failed",
			slog.String("user", id.Username), slog.Any("error", err))
		writeError(w, http.StatusInternalServerError, "access review failed")
		return auth.Identity{}, false
	}
	if !allowed {
		writeError(w, http.StatusForbidden,
			"user "+id.Username+" may not "+verb+" evaluations in "+s.namespace)
		return auth.Identity{}, false
	}
	return *id, true
}

// writeJSON writes v with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes a SubmissionError-shaped body.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, evaluation.SubmissionError{Error: msg})
}
