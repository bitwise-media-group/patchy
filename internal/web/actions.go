// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package web

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/action"
	"github.com/bitwise-media-group/patchy/internal/web/auth"
	"github.com/bitwise-media-group/patchy/internal/web/authz"
)

// approveNote records where a status-page approval came from.
const approveNote = "Approved from the status page."

// statusError carries an HTTP status and a human-facing message out of the
// action write path; the 403 bodies become UI toasts verbatim.
type statusError struct {
	code int
	msg  string
}

func (e *statusError) Error() string { return e.msg }

// handleAction runs one human action. Authentication and the RBAC grant are
// checked first; the finding's own state machine is re-validated on every
// conflict retry against fresh state.
func (s *Server) handleAction(w http.ResponseWriter, r *http.Request) {
	name, verb := r.PathValue("name"), r.PathValue("verb")
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
	if !slices.Contains(authz.ActionVerbs, verb) {
		http.NotFound(w, r)
		return
	}
	grants, err := s.granter.Grants(r.Context(), *id)
	if err != nil {
		s.log.LogAttrs(r.Context(), slog.LevelError, "grants failed", slog.Any("error", err))
		http.Error(w, "authorization failed", http.StatusInternalServerError)
		return
	}
	if !grants.Allows(verb) {
		http.Error(w, fmt.Sprintf("Permission denied. User %q may not %s findings in namespace %q.",
			id.Display(), verb, s.namespace), http.StatusForbidden)
		return
	}

	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var cur v1alpha1.Finding
		if err := s.client.Get(r.Context(), types.NamespacedName{Namespace: s.namespace, Name: name}, &cur); err != nil {
			return err
		}
		return s.apply(r, &cur, verb, *id)
	})
	switch {
	case err == nil:
		s.log.LogAttrs(r.Context(), slog.LevelInfo, "finding action applied",
			slog.String("finding", name), slog.String("verb", verb), slog.String("user", id.Username))
		writeJSON(w, map[string]any{})
	case apierrors.IsNotFound(err):
		http.Error(w, fmt.Sprintf("finding %s not found", name), http.StatusNotFound)
	default:
		var se *statusError
		if errors.As(err, &se) {
			http.Error(w, se.msg, se.code)
			return
		}
		s.log.LogAttrs(r.Context(), slog.LevelError, "finding action failed",
			slog.String("finding", name), slog.String("verb", verb), slog.Any("error", err))
		http.Error(w, "action failed", http.StatusInternalServerError)
	}
}

// apply performs one verb against fresh state. The gating lives in
// internal/action so the CLI applies identical semantics; this maps its
// outcomes onto HTTP and writes. A no-op (the action's effect is already
// recorded) skips the write and still reports success.
func (s *Server) apply(r *http.Request, cur *v1alpha1.Finding, verb string, id auth.Identity) error {
	changed, err := action.Apply(cur, verb, id.Username, approveNote, s.now())
	switch {
	case errors.Is(err, action.ErrUnavailable):
		return &statusError{
			code: http.StatusForbidden,
			msg: fmt.Sprintf("Action %s is not available for finding %s (phase %s).",
				verb, cur.Name, cur.Status.Phase),
		}
	case errors.Is(err, action.ErrUnknownVerb):
		return &statusError{code: http.StatusNotFound, msg: "unknown action"}
	case err != nil:
		return err
	case !changed:
		return nil
	}
	return s.client.Update(r.Context(), cur)
}
