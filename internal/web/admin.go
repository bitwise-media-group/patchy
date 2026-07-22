// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package web

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/web/auth"
	"github.com/bitwise-media-group/patchy/internal/web/authz"
)

// handleAdmin runs one namespace-wide action (replay, reset) — the demo
// tooling behind the user menu. Same envelope as the per-finding actions:
// authenticate, resolve the RBAC grant, act.
func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	verb := r.PathValue("verb")
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
	if !grants.AllowsAdmin(verb) {
		http.Error(w, fmt.Sprintf("Permission denied. User %q may not %s namespace %q.",
			id.Display(), verb, s.namespace), http.StatusForbidden)
		return
	}

	switch verb {
	case authz.VerbReplay:
		err = s.requestReplay(r.Context(), *id)
	case authz.VerbReset:
		err = s.resetAll(r.Context(), *id)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		var se *statusError
		if errors.As(err, &se) {
			http.Error(w, se.msg, se.code)
			return
		}
		s.log.LogAttrs(r.Context(), slog.LevelError, "admin action failed",
			slog.String("verb", verb), slog.Any("error", err))
		http.Error(w, "action failed", http.StatusInternalServerError)
		return
	}
	s.log.LogAttrs(r.Context(), slog.LevelInfo, "admin action applied",
		slog.String("verb", verb), slog.String("user", id.Username))
	writeJSON(w, map[string]any{})
}

// requestReplay stamps spec.replay on every active Integration; the
// integration-controller performs the actual redelivery on its next
// reconcile (the status server holds no forge credential).
func (s *Server) requestReplay(ctx context.Context, id auth.Identity) error {
	var list v1alpha1.IntegrationList
	if err := s.client.List(ctx, &list, client.InNamespace(s.namespace)); err != nil {
		return fmt.Errorf("list integrations: %w", err)
	}
	requested := 0
	for i := range list.Items {
		if list.Items[i].Spec.Suspend {
			continue
		}
		name := list.Items[i].Name
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			var cur v1alpha1.Integration
			if err := s.client.Get(ctx, client.ObjectKeyFromObject(&list.Items[i]), &cur); err != nil {
				return err
			}
			cur.Spec.Replay = &v1alpha1.ActionRequest{By: id.Username, At: metav1.NewTime(s.now())}
			return s.client.Update(ctx, &cur)
		})
		if err != nil {
			return fmt.Errorf("request replay on integration %s: %w", name, err)
		}
		requested++
	}
	if requested == 0 {
		return &statusError{code: http.StatusConflict, msg: "No active integration to replay."}
	}
	return nil
}

// resetAll requests the demo reset by stamping spec.reset on every active
// Integration; the integration-controller consumes it — permanently
// deleting the tracking issues, reopening the dismissed code-scanning
// alerts, deleting every pipeline resource, and dropping its receiver's
// delivery dedup window — with credentials the status server does not
// hold. The controller also needs the Findings' issue numbers and
// repositories, which is why nothing is deleted here first. A namespace
// with no active Integration has nothing on GitHub to clean, so the
// pipeline resources are deleted directly as a fallback.
func (s *Server) resetAll(ctx context.Context, id auth.Identity) error {
	var list v1alpha1.IntegrationList
	if err := s.client.List(ctx, &list, client.InNamespace(s.namespace)); err != nil {
		return fmt.Errorf("list integrations: %w", err)
	}
	stamped := 0
	for i := range list.Items {
		if list.Items[i].Spec.Suspend {
			continue
		}
		name := list.Items[i].Name
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			var cur v1alpha1.Integration
			if err := s.client.Get(ctx, client.ObjectKeyFromObject(&list.Items[i]), &cur); err != nil {
				return err
			}
			cur.Spec.Reset = &v1alpha1.ActionRequest{By: id.Username, At: metav1.NewTime(s.now())}
			return s.client.Update(ctx, &cur)
		})
		if err != nil {
			return fmt.Errorf("request reset on integration %s: %w", name, err)
		}
		stamped++
	}
	if stamped > 0 {
		return nil
	}

	for _, obj := range []client.Object{
		&v1alpha1.Finding{},
		&v1alpha1.Investigation{},
		&v1alpha1.Remediation{},
		&v1alpha1.Repository{},
		&v1alpha1.FindingRollup{},
	} {
		if err := s.client.DeleteAllOf(ctx, obj, client.InNamespace(s.namespace)); err != nil {
			return fmt.Errorf("delete all %T: %w", obj, err)
		}
	}
	return nil
}
