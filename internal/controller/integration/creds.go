// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package integration

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/forge"
	"github.com/bitwise-media-group/patchy/internal/ghclient"
	"github.com/bitwise-media-group/patchy/internal/ghsecret"
)

// Sentinel errors for integration selection.
var (
	// ErrNoIntegration: no enabled Integration provides the capability.
	ErrNoIntegration = errors.New("no integration provides the capability")
	// ErrAmbiguousIntegration: several Integrations provide the capability —
	// v1alpha1 requires exactly one per namespace.
	ErrAmbiguousIntegration = errors.New("multiple integrations provide the capability")
)

// Creds reads Integration credential Secrets and builds GitHub clients.
type Creds struct {
	c    client.Reader
	apps *ghsecret.Apps
}

// NewCreds builds a Creds reading through r (the manager's API reader so
// Secrets stay uncached).
func NewCreds(r client.Reader) *Creds {
	return &Creds{c: r, apps: ghsecret.NewApps()}
}

// secret fetches the Integration's credential Secret.
func (c *Creds) secret(ctx context.Context, integ *v1alpha1.Integration) (*corev1.Secret, error) {
	var secret corev1.Secret
	key := types.NamespacedName{Namespace: integ.Namespace, Name: integ.Spec.SecretRef.Name}
	if err := c.c.Get(ctx, key, &secret); err != nil {
		return nil, fmt.Errorf("get integration secret %s: %w", key, err)
	}
	return &secret, nil
}

// WebhookSecret returns the Integration's receiver HMAC secret.
func (c *Creds) WebhookSecret(ctx context.Context, integ *v1alpha1.Integration) ([]byte, error) {
	secret, err := c.secret(ctx, integ)
	if err != nil {
		return nil, err
	}
	wh := secret.Data[ghsecret.KeyWebhookSecret]
	if len(wh) == 0 {
		return nil, fmt.Errorf("secret %s/%s missing key %s",
			secret.Namespace, secret.Name, ghsecret.KeyWebhookSecret)
	}
	return wh, nil
}

// Client returns an API client for the Integration, authenticated for repo
// (App installation) or statically (PAT).
func (c *Creds) Client(ctx context.Context, integ *v1alpha1.Integration, repo ghclient.Repo) (*ghclient.Client, error) {
	secret, err := c.secret(ctx, integ)
	if err != nil {
		return nil, err
	}
	baseURL := githubBaseURL(integ)
	if tok, ok := ghsecret.Token(secret); ok {
		return ghclient.NewToken(tok, baseURL)
	}
	app, err := c.apps.FromSecret(secret, baseURL)
	if err != nil {
		return nil, err
	}
	return app.Installation(ctx, repo)
}

// Validate checks the Integration's secret carries a usable API credential
// and a webhook secret.
func (c *Creds) Validate(ctx context.Context, integ *v1alpha1.Integration) error {
	secret, err := c.secret(ctx, integ)
	if err != nil {
		return err
	}
	if err := c.apps.Validate(secret, githubBaseURL(integ)); err != nil {
		return err
	}
	if len(secret.Data[ghsecret.KeyWebhookSecret]) == 0 {
		return fmt.Errorf("secret %s/%s missing key %s",
			secret.Namespace, secret.Name, ghsecret.KeyWebhookSecret)
	}
	return nil
}

// githubBaseURL returns the Integration's GHES base URL, empty for
// github.com.
func githubBaseURL(integ *v1alpha1.Integration) string {
	if integ.Spec.GitHub == nil {
		return ""
	}
	return integ.Spec.GitHub.BaseURL
}

// githubHost returns the host the Integration's repositories live on.
func githubHost(integ *v1alpha1.Integration) string {
	f := v1alpha1.Forge{Spec: v1alpha1.ForgeSpec{BaseURL: githubBaseURL(integ)}}
	return forge.Host(&f)
}

// capability selects Integrations by what they provide.
type capability func(*v1alpha1.Integration) bool

// issuesEnabled reports whether the Integration projects tracking issues.
func issuesEnabled(i *v1alpha1.Integration) bool {
	return !i.Spec.Suspend && i.Spec.GitHub != nil &&
		i.Spec.GitHub.Issues != nil && i.Spec.GitHub.Issues.Enabled
}

// codeScanningEnabled reports whether the Integration ingests code-scanning
// alerts.
func codeScanningEnabled(i *v1alpha1.Integration) bool {
	return !i.Spec.Suspend && i.Spec.GitHub != nil &&
		i.Spec.GitHub.CodeScanningAlerts != nil && i.Spec.GitHub.CodeScanningAlerts.Enabled
}

// selectIntegration returns the single Integration in namespace providing
// the capability (the v1alpha1 singleton rule).
func selectIntegration(
	ctx context.Context, r client.Reader, namespace string, has capability,
) (*v1alpha1.Integration, error) {
	var list v1alpha1.IntegrationList
	if err := r.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("list integrations: %w", err)
	}
	var won []*v1alpha1.Integration
	for i := range list.Items {
		if has(&list.Items[i]) {
			won = append(won, &list.Items[i])
		}
	}
	switch len(won) {
	case 0:
		return nil, ErrNoIntegration
	case 1:
		return won[0], nil
	default:
		return nil, ErrAmbiguousIntegration
	}
}
