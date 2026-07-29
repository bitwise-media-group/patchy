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
	"github.com/bitwise-media-group/patchy/internal/integrationcap"
	"github.com/bitwise-media-group/patchy/internal/wiz"
)

// Sentinel errors for integration selection, shared with every other
// capability consumer through integrationcap.
var (
	// ErrNoIntegration: no enabled Integration provides the capability.
	ErrNoIntegration = integrationcap.ErrNoIntegration
	// ErrAmbiguousIntegration: several Integrations provide the capability —
	// v1alpha1 requires exactly one per namespace.
	ErrAmbiguousIntegration = integrationcap.ErrAmbiguousIntegration
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

// ErrNoCredential reports that the Integration names no credential Secret.
// Legal — a google-cloud integration authenticates its inbound deliveries by
// OIDC and calls no API — so callers that only optionally need one check for
// it rather than treating it as a failure.
var ErrNoCredential = errors.New("integration has no credential secret")

// secret fetches the Integration's credential Secret.
func (c *Creds) secret(ctx context.Context, integ *v1alpha1.Integration) (*corev1.Secret, error) {
	if integ.Spec.SecretRef == nil {
		return nil, fmt.Errorf("integration %s: %w", integ.Name, ErrNoCredential)
	}
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

// App returns the Integration's GitHub App for app-level (JWT) calls like
// the webhook delivery log. ok is false for a PAT credential — PATs cannot
// see App webhook deliveries.
func (c *Creds) App(ctx context.Context, integ *v1alpha1.Integration) (app *ghclient.App, ok bool, err error) {
	secret, err := c.secret(ctx, integ)
	if err != nil {
		return nil, false, err
	}
	if _, isPAT := ghsecret.Token(secret); isPAT {
		return nil, false, nil
	}
	app, err = c.apps.FromSecret(secret, githubBaseURL(integ))
	if err != nil {
		return nil, false, err
	}
	return app, true, nil
}

// WizWebhookToken returns the shared bearer token the Integration's inbound
// Wiz deliveries must carry.
func (c *Creds) WizWebhookToken(ctx context.Context, integ *v1alpha1.Integration) ([]byte, error) {
	secret, err := c.secret(ctx, integ)
	if err != nil {
		return nil, err
	}
	token := secret.Data[wiz.KeyWebhookToken]
	if len(token) == 0 {
		return nil, fmt.Errorf("secret %s/%s missing key %s",
			secret.Namespace, secret.Name, wiz.KeyWebhookToken)
	}
	return token, nil
}

// WizAPICreds returns the Wiz API service-account credentials for write-back.
func (c *Creds) WizAPICreds(
	ctx context.Context, integ *v1alpha1.Integration,
) (clientID, clientSecret string, err error) {
	secret, err := c.secret(ctx, integ)
	if err != nil {
		return "", "", err
	}
	id, sec := secret.Data[wiz.KeyClientID], secret.Data[wiz.KeyClientSecret]
	if len(id) == 0 || len(sec) == 0 {
		return "", "", fmt.Errorf("secret %s/%s missing key %s or %s",
			secret.Namespace, secret.Name, wiz.KeyClientID, wiz.KeyClientSecret)
	}
	return string(id), string(sec), nil
}

// Validate checks the Integration is usable, by provider: github needs a
// secret carrying an API credential and a webhook secret; google-cloud and
// aws hold no credential at all, so only their settings are checked; wiz
// needs its webhook token, plus API credentials when write-back is
// configured.
func (c *Creds) Validate(ctx context.Context, integ *v1alpha1.Integration) error {
	switch integ.Spec.Provider {
	case v1alpha1.IntegrationProviderGoogleCloud:
		return validateGoogleCloud(integ)
	case v1alpha1.IntegrationProviderWiz:
		return c.validateWiz(ctx, integ)
	case v1alpha1.IntegrationProviderAWS:
		return validateAWS(integ)
	}
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

// validateGoogleCloud checks the settings the receiver needs to authenticate
// a Pub/Sub push. The CEL schema already requires the fields each capability
// needs when its block is present; this catches a block that is enabled with
// every capability omitted.
func validateGoogleCloud(integ *v1alpha1.Integration) error {
	gc := integ.Spec.GoogleCloud
	if gc == nil || (gc.SecurityCommandCenter == nil && gc.CloudAssetInventory == nil) {
		return errors.New("google-cloud integration enables no capability")
	}
	scc := gc.SecurityCommandCenter
	if scc == nil || !scc.Enabled {
		return nil
	}
	if scc.Audience == "" || scc.ServiceAccount == "" {
		return errors.New(
			"securityCommandCenter needs both audience and serviceAccount to authenticate the push subscription")
	}
	return nil
}

// validateAWS checks the aws Integration enables a capability. The CEL
// schema already enforces exactly one inventory backend; whether that
// backend is reachable is the enhancer's own startup check, because the
// integration-controller holds no AWS credential to ask with.
func validateAWS(integ *v1alpha1.Integration) error {
	if integ.Spec.AWS == nil || integ.Spec.AWS.ResourceTags == nil {
		return errors.New("aws integration enables no capability")
	}
	return nil
}

// validateWiz checks the wiz Integration's credential Secret: the webhook
// token always, the API service account only when write-back is configured.
func (c *Creds) validateWiz(ctx context.Context, integ *v1alpha1.Integration) error {
	w := integ.Spec.Wiz
	if w == nil || (w.Issues == nil && w.Defend == nil) {
		return errors.New("wiz integration enables no capability")
	}
	if _, err := c.WizWebhookToken(ctx, integ); err != nil {
		return err
	}
	if w.API != nil {
		if _, _, err := c.WizAPICreds(ctx, integ); err != nil {
			return err
		}
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
type capability = integrationcap.Capability

// issuesEnabled reports whether the Integration projects tracking issues.
func issuesEnabled(i *v1alpha1.Integration) bool {
	return !i.Spec.Suspend && i.Spec.GitHub != nil &&
		i.Spec.GitHub.Issues != nil && i.Spec.GitHub.Issues.Enabled
}

// redeliveryEnabled reports whether the Integration sweeps failed webhook
// deliveries.
func redeliveryEnabled(i *v1alpha1.Integration) bool {
	return !i.Spec.Suspend && i.Spec.GitHub != nil &&
		i.Spec.GitHub.Redelivery != nil && i.Spec.GitHub.Redelivery.Enabled
}

// codeScanningEnabled reports whether the Integration ingests code-scanning
// alerts.
func codeScanningEnabled(i *v1alpha1.Integration) bool {
	return !i.Spec.Suspend && i.Spec.GitHub != nil &&
		i.Spec.GitHub.CodeScanningAlerts != nil && i.Spec.GitHub.CodeScanningAlerts.Enabled
}

// sccEnabled reports whether the Integration ingests Security Command Center
// findings from a Pub/Sub push subscription.
func sccEnabled(i *v1alpha1.Integration) bool {
	return !i.Spec.Suspend && i.Spec.GoogleCloud != nil &&
		i.Spec.GoogleCloud.SecurityCommandCenter != nil &&
		i.Spec.GoogleCloud.SecurityCommandCenter.Enabled
}

// wizIssuesEnabled reports whether the Integration ingests Wiz Issues.
func wizIssuesEnabled(i *v1alpha1.Integration) bool {
	return !i.Spec.Suspend && i.Spec.Wiz != nil &&
		i.Spec.Wiz.Issues != nil && i.Spec.Wiz.Issues.Enabled
}

// wizDefendEnabled reports whether the Integration ingests Wiz Defend threat
// detections.
func wizDefendEnabled(i *v1alpha1.Integration) bool {
	return !i.Spec.Suspend && i.Spec.Wiz != nil &&
		i.Spec.Wiz.Defend != nil && i.Spec.Wiz.Defend.Enabled
}

// selectIntegration returns the single Integration in namespace providing
// the capability (the v1alpha1 singleton rule).
func selectIntegration(
	ctx context.Context, r client.Reader, namespace string, has capability,
) (*v1alpha1.Integration, error) {
	return integrationcap.Select(ctx, r, namespace, has)
}
