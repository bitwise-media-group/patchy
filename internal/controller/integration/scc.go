// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package integration

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/scc"
	"github.com/bitwise-media-group/patchy/internal/webhook"
)

// SCCPath is the receiver route Pub/Sub push subscriptions target. It matches
// the path integration_controller advertises on status.webhookPath, which is
// derived from the provider name.
const SCCPath = "/" + string(v1alpha1.IntegrationProviderGoogleCloud) + "/webhooks"

// sccAuthenticator validates a Pub/Sub push against the google-cloud
// Integration's audience and service account. Both are read per request
// rather than captured at startup: they are cluster configuration an operator
// can change, and the webhook secrets on the GitHub route work the same way.
type sccAuthenticator struct {
	// Reader lists Integrations (informer cache).
	Reader client.Reader
	// Namespace the Integrations live in.
	Namespace string
	// Issuer overrides the OIDC issuer verified against. Empty means Google,
	// which is the only correct value in production; the e2e suite points it
	// at a local issuer so the authentication path is exercised for real
	// rather than stubbed out.
	Issuer string
	// NewVerifier builds a token verifier for an audience; nil means OIDC
	// discovery against Issuer. Unit tests substitute one that verifies
	// locally-signed tokens, so they need no network at all.
	NewVerifier func(audience string) webhook.TokenVerifier

	mu        sync.Mutex
	verifiers map[string]webhook.TokenVerifier
}

// Authenticate implements webhook.Authenticator.
func (a *sccAuthenticator) Authenticate(ctx context.Context, r *http.Request, body []byte) error {
	integ, err := selectIntegration(ctx, a.Reader, a.Namespace, sccEnabled)
	if err != nil {
		// No configured integration means nothing may deliver here. Failing
		// closed matters: this route is internet-facing.
		return fmt.Errorf("%w: %w", webhook.ErrUnauthenticated, err)
	}
	cfg := integ.Spec.GoogleCloud.SecurityCommandCenter
	inner := &webhook.GoogleOIDCAuthenticator{
		Verify:         a.verifierFor(cfg.Audience),
		Audience:       cfg.Audience,
		ServiceAccount: cfg.ServiceAccount,
	}
	return inner.Authenticate(ctx, r, body)
}

// verifierFor returns the cached verifier for an audience, building one on
// first use. Building fetches Google's discovery document, so it must not
// happen per delivery.
func (a *sccAuthenticator) verifierFor(audience string) webhook.TokenVerifier {
	a.mu.Lock()
	defer a.mu.Unlock()
	if v, ok := a.verifiers[audience]; ok {
		return v
	}
	build := a.NewVerifier
	if build == nil {
		build = func(aud string) webhook.TokenVerifier {
			v := webhook.NewGoogleVerifier(aud)
			v.Issuer = a.Issuer
			return v
		}
	}
	v := build(audience)
	if a.verifiers == nil {
		a.verifiers = map[string]webhook.TokenVerifier{}
	}
	a.verifiers[audience] = v
	return v
}

// sccDecoder labels a Pub/Sub push. The route carries exactly one kind of
// delivery, so the event type is a constant rather than something read off
// the request; the delivery id is the Pub/Sub message id, which lives in the
// body because Pub/Sub sets no custom headers.
func sccDecoder(_ *http.Request, body []byte) (eventType, deliveryID string, err error) {
	return scc.EventType, scc.DeliveryID(body), nil
}
