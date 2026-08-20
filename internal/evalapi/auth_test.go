// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package evalapi

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

func writeAuthConfig(t *testing.T, yaml string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoadConfig(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		wantErr bool
		check   func(t *testing.T, cfg *Config)
	}{
		{
			name: "mode none",
			yaml: "mode: none\n",
			check: func(t *testing.T, cfg *Config) {
				if cfg.Mode != ModeNone {
					t.Errorf("mode = %q, want none", cfg.Mode)
				}
			},
		},
		{
			name: "oidc with claim defaults",
			yaml: "mode: oidc\noidc:\n  issuerURL: https://dex.local\n  clientID: evolve\n",
			check: func(t *testing.T, cfg *Config) {
				c := cfg.OIDC.Claims
				if c.Username != "email" || c.Groups != "groups" || c.DisplayName != "name" {
					t.Errorf("claim defaults = %q/%q/%q, want email/groups/name",
						c.Username, c.Groups, c.DisplayName)
				}
			},
		},
		{
			name: "oidc with explicit claims",
			yaml: "mode: oidc\noidc:\n  issuerURL: https://dex.local\n  clientID: evolve\n  claims:\n    username: sub\n",
			check: func(t *testing.T, cfg *Config) {
				if cfg.OIDC.Claims.Username != "sub" {
					t.Errorf("username claim = %q, want sub", cfg.OIDC.Claims.Username)
				}
				if cfg.OIDC.Claims.Groups != "groups" {
					t.Errorf("groups claim = %q, want the groups default", cfg.OIDC.Claims.Groups)
				}
			},
		},
		{name: "oidc missing issuer", yaml: "mode: oidc\noidc:\n  clientID: evolve\n", wantErr: true},
		{name: "oidc missing clientID", yaml: "mode: oidc\noidc:\n  issuerURL: https://dex.local\n", wantErr: true},
		{name: "oidc block absent", yaml: "mode: oidc\n", wantErr: true},
		{name: "unknown mode", yaml: "mode: cookie\n", wantErr: true},
		{name: "unknown field rejected", yaml: "mode: none\nsessions: true\n", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, err := LoadConfig(writeAuthConfig(t, c.yaml))
			if (err != nil) != c.wantErr {
				t.Fatalf("LoadConfig error = %v, wantErr %v", err, c.wantErr)
			}
			if c.check != nil {
				c.check(t, cfg)
			}
		})
	}
}

func TestLoadConfigMissing(t *testing.T) {
	if _, err := LoadConfig(""); err == nil {
		t.Error("LoadConfig(\"\") succeeded, want error")
	}
	if _, err := LoadConfig(filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Error("LoadConfig on a missing file succeeded, want error")
	}
}

func TestAuthInfo(t *testing.T) {
	none := Config{Mode: ModeNone}
	if info := none.AuthInfo(); info.Mode != ModeNone || info.Issuer != "" {
		t.Errorf("none AuthInfo = %+v, want bare mode", info)
	}

	oidc := Config{Mode: ModeOIDC, OIDC: &OIDCConfig{
		IssuerURL: "https://dex.local", ClientID: "evolve", Scopes: []string{"openid", "email"},
	}}
	info := oidc.AuthInfo()
	if info.Issuer != "https://dex.local" || info.ClientID != "evolve" || len(info.Scopes) != 2 {
		t.Errorf("oidc AuthInfo = %+v, want issuer/clientID/scopes carried through", info)
	}
}

func TestNewAuthenticatorModeNone(t *testing.T) {
	a, err := NewAuthenticator(t.Context(), &Config{Mode: ModeNone})
	if err != nil {
		t.Fatalf("NewAuthenticator: %v", err)
	}
	id, err := a.Identify(httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil || id == nil {
		t.Fatalf("Identify = (%v, %v), want a fixed identity", id, err)
	}
	if id.Username != "patchy-eval-dev" {
		t.Errorf("username = %q, want patchy-eval-dev", id.Username)
	}
}

func TestNewAuthenticatorUnknownMode(t *testing.T) {
	if _, err := NewAuthenticator(t.Context(), &Config{Mode: "cookie"}); err == nil {
		t.Error("NewAuthenticator with an unknown mode succeeded, want error")
	}
}

func TestBearerIdentify(t *testing.T) {
	iss := newFakeIssuer(t)
	cfg, err := LoadConfig(writeAuthConfig(t,
		"mode: oidc\noidc:\n  issuerURL: "+iss.url()+"\n  clientID: evolve\n"))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	a, err := NewAuthenticator(t.Context(), cfg)
	if err != nil {
		t.Fatalf("NewAuthenticator (discovery): %v", err)
	}

	valid := iss.mint(t, map[string]any{
		"aud": "evolve", "email": "dev@example.com", "name": "Dev", "groups": []string{"eng"},
	}, time.Hour)

	cases := []struct {
		name    string
		header  string
		wantID  string
		wantErr bool
		// anonymous: no credential presented → (nil, nil).
		anonymous bool
	}{
		{name: "valid token", header: "Bearer " + valid, wantID: "dev@example.com"},
		{name: "no header", anonymous: true},
		{name: "empty bearer", header: "Bearer ", anonymous: true},
		{name: "other scheme ignored", header: "Basic Zm9vOmJhcg==", anonymous: true},
		{name: "garbage token", header: "Bearer not.a.jwt", wantErr: true},
		{
			name:    "wrong audience",
			header:  "Bearer " + iss.mint(t, map[string]any{"aud": "someone-else", "email": "dev@example.com"}, time.Hour),
			wantErr: true,
		},
		{
			name:    "expired token",
			header:  "Bearer " + iss.mint(t, map[string]any{"aud": "evolve", "email": "dev@example.com"}, -time.Hour),
			wantErr: true,
		},
		{
			name:    "no usable username claim",
			header:  "Bearer " + iss.mint(t, map[string]any{"aud": "evolve"}, time.Hour),
			wantErr: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/evaluations", nil)
			if c.header != "" {
				req.Header.Set("Authorization", c.header)
			}
			id, err := a.Identify(req)
			if (err != nil) != c.wantErr {
				t.Fatalf("Identify error = %v, wantErr %v", err, c.wantErr)
			}
			if c.anonymous {
				if id != nil {
					t.Fatalf("Identify = %+v, want nil identity for an absent credential", id)
				}
				return
			}
			if c.wantErr {
				return
			}
			if id.Username != c.wantID {
				t.Errorf("username = %q, want %q", id.Username, c.wantID)
			}
			if len(id.Groups) != 1 || id.Groups[0] != "eng" {
				t.Errorf("groups = %v, want [eng]", id.Groups)
			}
		})
	}
}

// fakeIssuer is a minimal OIDC provider — discovery + JWKS — minting RS256 ID
// tokens directly (no token endpoint: the API only ever verifies).
type fakeIssuer struct {
	ts  *httptest.Server
	key *rsa.PrivateKey
}

func newFakeIssuer(t *testing.T) *fakeIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa key: %v", err)
	}
	fi := &fakeIssuer{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		issuerJSON(t, w, map[string]any{
			"issuer":                                fi.url(),
			"authorization_endpoint":                fi.url() + "/auth",
			"token_endpoint":                        fi.url() + "/token",
			"jwks_uri":                              fi.url() + "/keys",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("GET /keys", func(w http.ResponseWriter, _ *http.Request) {
		issuerJSON(t, w, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key: key.Public(), KeyID: "test", Algorithm: "RS256", Use: "sig",
		}}})
	})
	fi.ts = httptest.NewServer(mux)
	t.Cleanup(fi.ts.Close)
	return fi
}

func (fi *fakeIssuer) url() string { return fi.ts.URL }

// mint signs an ID token; iss/iat are filled in, everything else (aud
// included) comes from claims.
func (fi *fakeIssuer) mint(t *testing.T, claims map[string]any, expIn time.Duration) string {
	t.Helper()
	all := map[string]any{
		"iss": fi.url(),
		"iat": time.Now().Add(-time.Minute).Unix(),
		"exp": time.Now().Add(expIn).Unix(),
	}
	maps.Copy(all, claims)
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: fi.key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "test"))
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	payload, err := json.Marshal(all)
	if err != nil {
		t.Fatalf("claims: %v", err)
	}
	jws, err := signer.Sign(payload)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	raw, err := jws.CompactSerialize()
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	return raw
}

func issuerJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("encode: %v", err)
	}
}
