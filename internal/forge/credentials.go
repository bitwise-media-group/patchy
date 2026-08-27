// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package forge

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/ghclient"
	"github.com/bitwise-media-group/patchy/internal/ghsecret"
)

// Secret keys a Forge credential Secret may carry (shared shape with
// Integration secrets — see internal/ghsecret).
const (
	SecretKeyToken      = ghsecret.KeyToken
	SecretKeyAppID      = ghsecret.KeyAppID
	SecretKeyPrivateKey = ghsecret.KeyPrivateKey
)

// Scope is the credential scope a caller needs.
type Scope string

// Credential scopes: read mints contents:read (clone/tarball); write mints
// contents:write (branch push) — pull-request creation rides the same
// installation client.
const (
	ScopeRead  Scope = "read"
	ScopeWrite Scope = "write"
)

// Store resolves Forges and mints their credentials.
type Store struct {
	c    client.Reader
	apps *ghsecret.Apps
}

// NewStore builds a Store reading Forges and Secrets through r. Pass the
// manager's API reader (not the cache) so Secrets need no list/watch grant.
func NewStore(r client.Reader) *Store {
	return &Store{c: r, apps: ghsecret.NewApps()}
}

// Resolve lists the Forges in namespace and picks the one covering repoURL.
func (s *Store) Resolve(ctx context.Context, namespace, repoURL string) (*Resolved, error) {
	var forges v1alpha1.ForgeList
	if err := s.c.List(ctx, &forges, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("list forges: %w", err)
	}
	return Resolve(forges.Items, repoURL)
}

// Token mints a short-lived token for the resolved repository at the given
// scope. With App auth the token is installation-scoped to the single
// repository; with a PAT the static token is returned as-is (the PAT cannot
// be narrowed — dev only).
func (s *Store) Token(ctx context.Context, res *Resolved, scope Scope) (string, time.Time, error) {
	secret, err := s.secret(ctx, res.Forge)
	if err != nil {
		return "", time.Time{}, err
	}
	if tok, ok := ghsecret.Token(secret); ok {
		return tok, time.Time{}, nil
	}
	app, err := s.apps.FromSecret(secret, res.Forge.Spec.BaseURL, proxyURL(res.Forge))
	if err != nil {
		return "", time.Time{}, err
	}
	perms := ghclient.TokenPerms{Contents: string(scope)}
	return app.ScopedToken(ctx, res.Repo, perms)
}

// Client returns an API client authenticated for the resolved repository —
// the surface for archive downloads, head-SHA resolution, and pull requests.
func (s *Store) Client(ctx context.Context, res *Resolved) (*ghclient.Client, error) {
	secret, err := s.secret(ctx, res.Forge)
	if err != nil {
		return nil, err
	}
	if tok, ok := ghsecret.Token(secret); ok {
		purl, err := ghsecret.ProxyURL(secret, proxyURL(res.Forge))
		if err != nil {
			return nil, err
		}
		return ghclient.NewToken(tok, res.Forge.Spec.BaseURL, ghclient.WithProxy(purl))
	}
	app, err := s.apps.FromSecret(secret, res.Forge.Spec.BaseURL, proxyURL(res.Forge))
	if err != nil {
		return nil, err
	}
	return app.Installation(ctx, res.Repo)
}

// Validate checks the Forge's secret is usable: a non-empty PAT, or a
// parseable App credential, either way with a parseable proxy URL. The Forge
// reconciler calls this for the Ready condition.
func (s *Store) Validate(ctx context.Context, f *v1alpha1.Forge) error {
	secret, err := s.secret(ctx, f)
	if err != nil {
		return err
	}
	return s.apps.Validate(secret, f.Spec.BaseURL, proxyURL(f))
}

// ProxyURL returns the resolved Forge's effective proxy URL — the spec proxy
// with the Secret's basic-auth credentials attached — for callers building
// their own client (the write-path pusher). "" means no spec proxy; the
// environment applies.
func (s *Store) ProxyURL(ctx context.Context, res *Resolved) (string, error) {
	raw := proxyURL(res.Forge)
	if raw == "" {
		return "", nil
	}
	secret, err := s.secret(ctx, res.Forge)
	if err != nil {
		return "", err
	}
	return ghsecret.ProxyURL(secret, raw)
}

// proxyURL returns the Forge's raw spec proxy URL, "" when unset.
func proxyURL(f *v1alpha1.Forge) string {
	if f == nil || f.Spec.Proxy == nil {
		return ""
	}
	return f.Spec.Proxy.URL
}

// secret fetches the Forge's credential Secret from its own namespace.
func (s *Store) secret(ctx context.Context, f *v1alpha1.Forge) (*corev1.Secret, error) {
	var secret corev1.Secret
	key := types.NamespacedName{Namespace: f.Namespace, Name: f.Spec.SecretRef.Name}
	if err := s.c.Get(ctx, key, &secret); err != nil {
		return nil, fmt.Errorf("get forge secret %s: %w", key, err)
	}
	return &secret, nil
}
