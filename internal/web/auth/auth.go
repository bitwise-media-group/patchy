// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package auth

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
)

// Identity is the resolved requester. Username and Groups feed access
// reviews; DisplayName is what the UI shows.
type Identity struct {
	// Username is the subject access reviews run for.
	Username string
	// Groups are the subject's groups for access reviews.
	Groups []string
	// DisplayName is the human-facing name (falls back to Username).
	DisplayName string
	// Session reports whether the identity is backed by a sign-in session
	// (the UI renders the user menu and sign-out only for sessions).
	Session bool
}

// Display returns the name the UI should render.
func (id Identity) Display() string {
	if id.DisplayName != "" {
		return id.DisplayName
	}
	return id.Username
}

// Authenticator resolves the requester of an HTTP request.
type Authenticator interface {
	// Identify resolves the current session. It may write cookies (token
	// refresh, SPA state), hence the ResponseWriter. (nil, nil) means "no
	// session"; callers map that to 401 on protected routes.
	Identify(w http.ResponseWriter, r *http.Request) (*Identity, error)
	// Register adds the sign-in routes (/oauth2/*, /logout) to mux. Modes
	// without a sign-in surface register nothing.
	Register(mux *http.ServeMux)
}

// New builds the Authenticator for cfg. A nil cfg is the unconfigured
// posture. The context bounds OIDC issuer discovery.
func New(ctx context.Context, cfg *Config, log *slog.Logger) (Authenticator, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if cfg == nil {
		return unconfigured{}, nil
	}
	switch cfg.Mode {
	case ModeNone:
		return fixed{id: Identity{Username: "patchy-status-dev", DisplayName: "dev (auth disabled)"}}, nil
	case ModeAnonymous:
		return fixed{id: Identity{
			Username:    cfg.Anonymous.Username,
			Groups:      cfg.Anonymous.Groups,
			DisplayName: cfg.Anonymous.Username,
		}}, nil
	case ModeOIDC:
		return newOIDC(ctx, cfg, log)
	default:
		return nil, fmt.Errorf("auth mode %q is not none, anonymous, or oidc", cfg.Mode)
	}
}

// unconfigured is the no-config posture: nobody is ever identified and no
// sign-in surface exists. The SPA detects it by the absence of the provider
// cookie and explains that sign-in is not configured.
type unconfigured struct{}

func (unconfigured) Identify(http.ResponseWriter, *http.Request) (*Identity, error) {
	return nil, nil
}

func (unconfigured) Register(*http.ServeMux) {}

// fixed serves the none and anonymous modes: every request is the same
// identity, and there is nothing to sign in to.
type fixed struct {
	id Identity
}

func (f fixed) Identify(http.ResponseWriter, *http.Request) (*Identity, error) {
	id := f.id
	return &id, nil
}

func (fixed) Register(*http.ServeMux) {}
