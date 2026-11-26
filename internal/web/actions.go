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
	"github.com/bitwise-media-group/patchy/internal/ghuser"
	"github.com/bitwise-media-group/patchy/internal/web/auth"
	"github.com/bitwise-media-group/patchy/internal/web/authz"
	"github.com/bitwise-media-group/patchy/internal/web/ghlink"
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
	// The namespace grant, or the scoped maintainer grant plus a live
	// GitHub role check on the finding's repository.
	var via []slog.Attr
	if !grants.Allows(verb) {
		if !grants.AllowsMaintained(verb) {
			http.Error(w, fmt.Sprintf("Permission denied. User %q may not %s findings in namespace %q.",
				id.Display(), verb, s.namespace), http.StatusForbidden)
			return
		}
		attrs, se := s.authorizeMaintained(w, r, *id, name, verb)
		if se != nil {
			http.Error(w, se.msg, se.code)
			return
		}
		via = attrs
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
		attrs := append([]slog.Attr{
			slog.String("finding", name), slog.String("verb", verb), slog.String("user", id.Username),
		}, via...)
		s.log.LogAttrs(r.Context(), slog.LevelInfo, "finding action applied", attrs...)
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

// authorizeMaintained admits a holder of a scoped maintainer verb
// (approve-maintained, expedite-maintained) when GitHub reports their linked
// identity as admin or maintain on the finding's repository right now. The
// cached maintained set only decides which buttons show; this check is
// live. It returns the audit attributes for the log line.
func (s *Server) authorizeMaintained(
	w http.ResponseWriter, r *http.Request, id auth.Identity, name, verb string,
) ([]slog.Attr, *statusError) {
	var f v1alpha1.Finding
	if err := s.client.Get(r.Context(), types.NamespacedName{Namespace: s.namespace, Name: name}, &f); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, &statusError{code: http.StatusNotFound, msg: fmt.Sprintf("finding %s not found", name)}
		}
		return nil, &statusError{code: http.StatusInternalServerError, msg: "failed to load finding"}
	}
	if f.Spec.Repository == nil || f.Spec.Repository.Type != v1alpha1.RepositoryTypeGitHub {
		return nil, &statusError{code: http.StatusForbidden, msg: fmt.Sprintf(
			"Permission denied. Finding %s has no GitHub repository, so only the namespace-wide %s grant applies.",
			name, verb)}
	}
	if !s.githubEnabled() {
		return nil, &statusError{code: http.StatusForbidden,
			msg: "GitHub linking is not configured, so the maintainer grant cannot be checked."}
	}
	live, _ := s.userLinks(r, id)
	l, repo, ok := linkForFinding(&f, live)
	if !ok {
		return nil, &statusError{code: http.StatusForbidden, msg: fmt.Sprintf(
			"Connect GitHub on %s to %s findings on repositories you maintain.", repoHost(f.Spec.Repository.URL), verb)}
	}
	var res *ghuser.AuthorizeRepositoryResponse
	err := s.links.Call(r.Context(), w, l.link, func(token string) error {
		var err error
		res, err = s.ghUsers.AuthorizeRepository(r.Context(), ghuser.AuthorizeRepositoryRequest{
			Integration: l.integ.Name, AccessToken: token, Repo: repo,
		})
		return err
	})
	switch {
	case errors.Is(err, ghlink.ErrNotLinked):
		return nil, &statusError{code: http.StatusForbidden,
			msg: "Your GitHub connection has expired. Reconnect GitHub and try again."}
	case err != nil:
		s.log.LogAttrs(r.Context(), slog.LevelWarn, "github role check failed",
			slog.String("finding", name), slog.Any("error", err))
		return nil, &statusError{code: http.StatusBadGateway, msg: "The GitHub role check could not be completed."}
	case !ghuser.Maintains(res.Role):
		return nil, &statusError{code: http.StatusForbidden, msg: fmt.Sprintf(
			"Permission denied. Your role on %s is %s; %s on maintained repositories needs admin or maintain.",
			repo, res.Role, verb)}
	}
	return []slog.Attr{
		slog.String("via", "maintained"), slog.String("github.login", l.link.Login),
		slog.String("github.role", res.Role),
	}, nil
}
