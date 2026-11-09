// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package auth

import (
	"fmt"
	"os"
	"strings"
	"time"

	"go.yaml.in/yaml/v4"
)

// Modes the configuration file selects between.
const (
	// ModeNone disables authentication entirely: every request is a fixed
	// development identity and authorization is bypassed.
	ModeNone = "none"
	// ModeAnonymous serves every request as one fixed identity that still
	// passes through RBAC access reviews.
	ModeAnonymous = "anonymous"
	// ModeOIDC runs the SSO authorization-code flow.
	ModeOIDC = "oidc"
)

// DefaultSessionDuration bounds a session when the configuration does not.
const DefaultSessionDuration = 7 * 24 * time.Hour

// defaultScopes are requested when the configuration lists none.
// offline_access asks the provider for a refresh token so sessions outlive
// the ID token's expiry.
var defaultScopes = []string{"openid", "offline_access", "profile", "email", "groups"}

// Duration is a time.Duration that unmarshals from Go duration strings
// ("168h", "30m") — the YAML decoder has no native duration notion.
type Duration struct {
	time.Duration
}

// UnmarshalYAML parses a Go duration string.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("duration %q: %w", s, err)
	}
	d.Duration = v
	return nil
}

// Config is the mounted authentication configuration.
type Config struct {
	// Mode is none, anonymous, or oidc.
	Mode string `yaml:"mode"`
	// SessionDuration caps a session's lifetime (default 168h).
	SessionDuration Duration `yaml:"sessionDuration"`
	// Insecure drops the Secure flag from cookies for plain-HTTP local
	// development. Never set it behind a real ingress.
	Insecure bool `yaml:"insecure"`
	// Anonymous configures the fixed identity of mode anonymous.
	Anonymous *AnonymousConfig `yaml:"anonymous"`
	// OIDC configures the SSO provider of mode oidc.
	OIDC *OIDCConfig `yaml:"oidc"`
}

// AnonymousConfig is the fixed identity every request resolves to in mode
// anonymous. Access reviews still run for it, so cluster RBAC decides what
// the identity may see and do.
type AnonymousConfig struct {
	// Username of the fixed identity.
	Username string `yaml:"username"`
	// Groups of the fixed identity.
	Groups []string `yaml:"groups"`
}

// OIDCConfig is the SSO provider for mode oidc.
type OIDCConfig struct {
	// IssuerURL is the provider's issuer for discovery.
	IssuerURL string `yaml:"issuerURL"`
	// ClientID of the registered client.
	ClientID string `yaml:"clientID"`
	// ClientSecret of the registered client. The secret also keys the
	// cookie/state encryption, so rotating it invalidates live sessions.
	ClientSecret string `yaml:"clientSecret"`
	// ClientSecretFile reads the client secret from a file (e.g. a projected
	// Secret key) instead of inline.
	ClientSecretFile string `yaml:"clientSecretFile"`
	// Scopes requested at authorization (default: openid, offline_access,
	// profile, email, groups).
	Scopes []string `yaml:"scopes"`
	// AuthURLParams are extra authorize-endpoint query parameters.
	AuthURLParams map[string]string `yaml:"authURLParams"`
	// AutoLogin redirects an unauthenticated browser straight to the
	// provider instead of showing the sign-in panel.
	AutoLogin bool `yaml:"autoLogin"`
	// RedirectURL overrides the derived callback URL for deployments where
	// the fronting proxy's forwarded headers cannot be trusted.
	RedirectURL string `yaml:"redirectURL"`
	// Claims maps token claims onto the identity.
	Claims ClaimsConfig `yaml:"claims"`
}

// ClaimsConfig names the token claims the identity is read from. These are
// claim names, not expressions.
type ClaimsConfig struct {
	// Username claim access reviews run for (default email).
	Username string `yaml:"username"`
	// Groups claim (default groups).
	Groups string `yaml:"groups"`
	// DisplayName claim the UI shows (default name).
	DisplayName string `yaml:"displayName"`
}

// LoadConfig reads and validates the configuration at path. An empty path or
// a missing file returns (nil, nil) — the unconfigured posture; the caller
// logs the consequence. A present-but-invalid file is an error: a broken
// configuration must not silently downgrade to no authentication.
func LoadConfig(path string) (*Config, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("auth config %s: %w", path, err)
	}
	var cfg Config
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("auth config %s: %w", path, err)
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("auth config %s: %w", path, err)
	}
	cfg.applyDefaults()
	return &cfg, nil
}

// validate rejects configurations that would misbehave at request time.
func (c *Config) validate() error {
	switch c.Mode {
	case ModeNone:
		return nil
	case ModeAnonymous:
		if c.Anonymous == nil || c.Anonymous.Username == "" {
			return fmt.Errorf("mode anonymous requires anonymous.username")
		}
		return nil
	case ModeOIDC:
		o := c.OIDC
		if o == nil {
			return fmt.Errorf("mode oidc requires the oidc block")
		}
		if o.IssuerURL == "" || o.ClientID == "" {
			return fmt.Errorf("oidc requires issuerURL and clientID")
		}
		if o.ClientSecret == "" && o.ClientSecretFile == "" {
			return fmt.Errorf("oidc requires clientSecret or clientSecretFile")
		}
		if o.ClientSecret != "" && o.ClientSecretFile != "" {
			return fmt.Errorf("oidc clientSecret and clientSecretFile are mutually exclusive")
		}
		return nil
	case "":
		return fmt.Errorf("mode is required: none, anonymous, or oidc")
	default:
		return fmt.Errorf("mode %q is not none, anonymous, or oidc", c.Mode)
	}
}

// applyDefaults fills the optional knobs after validation.
func (c *Config) applyDefaults() {
	if c.SessionDuration.Duration <= 0 {
		c.SessionDuration.Duration = DefaultSessionDuration
	}
	if c.OIDC == nil {
		return
	}
	if len(c.OIDC.Scopes) == 0 {
		c.OIDC.Scopes = append([]string(nil), defaultScopes...)
	}
	cl := &c.OIDC.Claims
	if cl.Username == "" {
		cl.Username = "email"
	}
	if cl.Groups == "" {
		cl.Groups = "groups"
	}
	if cl.DisplayName == "" {
		cl.DisplayName = "name"
	}
}

// clientSecret resolves the inline or file-based client secret.
func (o *OIDCConfig) clientSecret() (string, error) {
	if o.ClientSecret != "" {
		return o.ClientSecret, nil
	}
	raw, err := os.ReadFile(o.ClientSecretFile)
	if err != nil {
		return "", fmt.Errorf("oidc clientSecretFile: %w", err)
	}
	secret := strings.TrimSpace(string(raw))
	if secret == "" {
		return "", fmt.Errorf("oidc clientSecretFile %s is empty", o.ClientSecretFile)
	}
	return secret, nil
}
