// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package evalapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"gopkg.in/yaml.v3"

	"github.com/bitwise-media-group/patchy/internal/web/auth"
	"github.com/bitwise-media-group/patchy/pkg/evaluation"
)

// Auth modes.
const (
	ModeNone = "none"
	ModeOIDC = "oidc"
)

// Config is the evaluation API's auth configuration — a trimmed sibling of
// web/auth.Config: no sessions, no cookies, no client secret (evolve is a
// public PKCE client; the API only verifies tokens).
type Config struct {
	// Mode is "none" (dev/e2e: fixed identity, authorization bypassed at
	// wiring) or "oidc".
	Mode string `yaml:"mode"`
	// OIDC configures bearer verification; required for mode oidc.
	OIDC *OIDCConfig `yaml:"oidc"`
}

// OIDCConfig verifies bearer tokens against one issuer.
type OIDCConfig struct {
	// IssuerURL for discovery.
	IssuerURL string `yaml:"issuerURL"`
	// ClientID the token audience must match — the public client evolve
	// logs in as.
	ClientID string `yaml:"clientID"`
	// Scopes advertised to clients via /auth/info.
	Scopes []string `yaml:"scopes"`
	// Claims maps token claims to username/groups (web/auth semantics).
	Claims auth.ClaimsConfig `yaml:"claims"`
}

// LoadConfig reads and validates the auth config. Unlike the status server
// there is no unconfigured fallback — every route except auth/info is
// authenticated, so a missing file is a startup error.
func LoadConfig(path string) (*Config, error) {
	if path == "" {
		return nil, errors.New("evalapi: auth config path is required")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("evalapi: auth config: %w", err)
	}
	defer func() { _ = f.Close() }()

	var cfg Config
	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("evalapi: auth config %s: %w", path, err)
	}
	switch cfg.Mode {
	case ModeNone:
	case ModeOIDC:
		if cfg.OIDC == nil || cfg.OIDC.IssuerURL == "" || cfg.OIDC.ClientID == "" {
			return nil, errors.New("evalapi: mode oidc requires oidc.issuerURL and oidc.clientID")
		}
	default:
		return nil, fmt.Errorf("evalapi: unknown auth mode %q (expected none or oidc)", cfg.Mode)
	}
	if cfg.OIDC != nil {
		if cfg.OIDC.Claims.Username == "" {
			cfg.OIDC.Claims.Username = "email"
		}
		if cfg.OIDC.Claims.Groups == "" {
			cfg.OIDC.Claims.Groups = "groups"
		}
		if cfg.OIDC.Claims.DisplayName == "" {
			cfg.OIDC.Claims.DisplayName = "name"
		}
	}
	return &cfg, nil
}

// AuthInfo renders the discovery document served at /api/v1/auth/info.
func (c *Config) AuthInfo() evaluation.AuthInfo {
	info := evaluation.AuthInfo{Mode: c.Mode}
	if c.OIDC != nil {
		info.Issuer = c.OIDC.IssuerURL
		info.ClientID = c.OIDC.ClientID
		info.Scopes = c.OIDC.Scopes
	}
	return info
}

// Authenticator resolves a request to an identity. A nil identity with a nil
// error means no usable credential was presented (→ 401).
type Authenticator interface {
	Identify(r *http.Request) (*auth.Identity, error)
}

// NewAuthenticator builds the mode's authenticator. Mode oidc runs issuer
// discovery, bounded by ctx.
func NewAuthenticator(ctx context.Context, cfg *Config) (Authenticator, error) {
	switch cfg.Mode {
	case ModeNone:
		return fixed{id: auth.Identity{Username: "patchy-eval-dev", DisplayName: "dev (auth disabled)"}}, nil
	case ModeOIDC:
		provider, err := gooidc.NewProvider(ctx, cfg.OIDC.IssuerURL)
		if err != nil {
			return nil, fmt.Errorf("evalapi: oidc discovery %s: %w", cfg.OIDC.IssuerURL, err)
		}
		return &BearerAuthenticator{
			verifier: provider.Verifier(&gooidc.Config{ClientID: cfg.OIDC.ClientID}),
			claims:   cfg.OIDC.Claims,
		}, nil
	default:
		return nil, fmt.Errorf("evalapi: unknown auth mode %q", cfg.Mode)
	}
}

// fixed always answers with one identity (mode none).
type fixed struct{ id auth.Identity }

func (f fixed) Identify(*http.Request) (*auth.Identity, error) {
	id := f.id
	return &id, nil
}

// BearerAuthenticator verifies Authorization: Bearer ID tokens against the
// configured issuer and maps claims to an identity.
type BearerAuthenticator struct {
	verifier *gooidc.IDTokenVerifier
	claims   auth.ClaimsConfig
}

// Identify verifies the bearer token. Absent header → (nil, nil); a present
// but invalid token is an error.
func (b *BearerAuthenticator) Identify(r *http.Request) (*auth.Identity, error) {
	raw, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !found || raw == "" {
		return nil, nil
	}
	token, err := b.verifier.Verify(r.Context(), raw)
	if err != nil {
		return nil, fmt.Errorf("verify bearer token: %w", err)
	}
	var claims map[string]any
	if err := token.Claims(&claims); err != nil {
		return nil, fmt.Errorf("decode token claims: %w", err)
	}
	return auth.MapClaims(claims, b.claims)
}
