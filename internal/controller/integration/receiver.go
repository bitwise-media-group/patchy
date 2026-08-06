// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package integration

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/ghas"
	"github.com/bitwise-media-group/patchy/internal/ghclient"
	"github.com/bitwise-media-group/patchy/internal/scc"
	"github.com/bitwise-media-group/patchy/internal/webhook"
	"github.com/bitwise-media-group/patchy/internal/wiz"
	pkggeneric "github.com/bitwise-media-group/patchy/pkg/generic"
	"github.com/bitwise-media-group/patchy/pkg/source"
)

// GitHubPath is the receiver route GitHub webhooks target.
const GitHubPath = "/" + string(v1alpha1.IntegrationProviderGitHub) + "/webhooks"

// Receiver turns validated provider deliveries into Finding writes. Scanner
// and cloud findings go through the pkg/source handler seam into the
// Ingestor; tracking events (issues, comments, pull requests) go to the
// Signals handler. It serves one webhook.Endpoint per provider.
type Receiver struct {
	// Reader lists Integrations (informer cache).
	Reader client.Reader
	// Creds builds Integration API clients.
	Creds *Creds
	// Ingest folds scanner findings into Finding resources.
	Ingest *Ingestor
	// Signals applies human-signal events to Findings.
	Signals *Signals
	// Namespace the Integrations and Findings live in.
	Namespace string
	// OIDCIssuer overrides the issuer the Pub/Sub push route verifies tokens
	// against; empty means Google, the only correct value in production.
	OIDCIssuer string
	// NewVerifier builds the OIDC token verifier for the Pub/Sub push route;
	// nil means discovery against OIDCIssuer.
	NewVerifier func(audience string) webhook.TokenVerifier
	// Log receives receiver diagnostics; nil discards.
	Log *slog.Logger
}

// Endpoints are the provider routes this receiver serves. Each names how its
// deliveries authenticate and how they are labelled; everything downstream —
// queueing, dedup, dispatch — is the server's, and identical for both.
func (r *Receiver) Endpoints() []webhook.Endpoint {
	return []webhook.Endpoint{
		{
			Path:    GitHubPath,
			Auth:    &webhook.HMACAuthenticator{SecretsFor: r.Secrets},
			Decode:  webhook.GitHubDecoder,
			Handler: webhook.HandlerFunc(r.handleGitHub),
		},
		{
			Path: SCCPath,
			Auth: &sccAuthenticator{
				Reader:      r.Reader,
				Namespace:   r.Namespace,
				Issuer:      r.OIDCIssuer,
				NewVerifier: r.NewVerifier,
			},
			Decode:  sccDecoder,
			Handler: webhook.HandlerFunc(r.handleSCC),
		},
		{
			Path:    WizPath,
			Auth:    &webhook.TokenAuthenticator{SecretsFor: r.WizTokens},
			Decode:  wizDecoder,
			Handler: webhook.HandlerFunc(r.handleWiz),
		},
		{
			Path: GenericPathPattern,
			Auth: &webhook.HMACAuthenticator{
				Header:            pkggeneric.SignatureHeader,
				SecretsForRequest: r.genericSecretsFor,
			},
			Decode:  genericDecoder,
			Handler: webhook.HandlerFunc(r.handleGeneric),
		},
	}
}

// Secrets returns every configured github Integration's webhook secret — the
// candidate set for delivery validation on the GitHub route.
func (r *Receiver) Secrets(ctx context.Context) [][]byte {
	return r.credentialSet(ctx, v1alpha1.IntegrationProviderGitHub, r.Creds.WebhookSecret)
}

// WizTokens returns every configured wiz Integration's webhook bearer token —
// the candidate set for delivery validation on the Wiz route.
func (r *Receiver) WizTokens(ctx context.Context) [][]byte {
	return r.credentialSet(ctx, v1alpha1.IntegrationProviderWiz, r.Creds.WizWebhookToken)
}

// credentialSet collects one inbound-delivery credential from every enabled
// Integration of a provider. An Integration whose credential cannot be read
// is logged and skipped — one broken Secret must not take the route down for
// the rest.
func (r *Receiver) credentialSet(
	ctx context.Context,
	provider v1alpha1.IntegrationProvider,
	get func(context.Context, *v1alpha1.Integration) ([]byte, error),
) [][]byte {
	var list v1alpha1.IntegrationList
	if err := r.Reader.List(ctx, &list, client.InNamespace(r.Namespace)); err != nil {
		r.log().LogAttrs(ctx, slog.LevelError, "list integrations for webhook credentials",
			slog.String("provider", string(provider)), slog.Any("error", err))
		return nil
	}
	var out [][]byte
	for i := range list.Items {
		integ := &list.Items[i]
		if integ.Spec.Provider != provider || integ.Spec.Suspend {
			continue
		}
		secret, err := get(ctx, integ)
		if err != nil {
			r.log().LogAttrs(ctx, slog.LevelWarn, "integration webhook credential unavailable",
				slog.String("integration", integ.Name), slog.Any("error", err))
			continue
		}
		out = append(out, secret)
	}
	return out
}

// handleGitHub dispatches one validated GitHub delivery by event type.
func (r *Receiver) handleGitHub(ctx context.Context, e webhook.Event) error {
	switch e.Type {
	case ghas.EventType:
		return r.handleScanner(ctx, e)
	case "issues", "issue_comment", "pull_request":
		integ, err := selectIntegration(ctx, r.Reader, r.Namespace, issuesEnabled)
		if err != nil {
			if errors.Is(err, ErrNoIntegration) {
				return nil // tracking not configured; nothing to apply
			}
			return err
		}
		return r.Signals.Handle(ctx, integ, e)
	default:
		return nil
	}
}

// handleScanner routes a scanner delivery through the source handler for the
// code-scanning Integration.
func (r *Receiver) handleScanner(ctx context.Context, e webhook.Event) error {
	integ, err := selectIntegration(ctx, r.Reader, r.Namespace, codeScanningEnabled)
	if err != nil {
		if errors.Is(err, ErrNoIntegration) {
			return nil
		}
		return err
	}
	return r.ingestAll(ctx, integ, ghas.New(&alertGetter{creds: r.Creds, integ: integ}), e)
}

// handleSCC routes a Security Command Center notification through the SCC
// source handler. Unlike the scanner path there is no API call: the
// notification carries everything the finding needs.
func (r *Receiver) handleSCC(ctx context.Context, e webhook.Event) error {
	integ, err := selectIntegration(ctx, r.Reader, r.Namespace, sccEnabled)
	if err != nil {
		if errors.Is(err, ErrNoIntegration) {
			return nil
		}
		return err
	}
	cfg := integ.Spec.GoogleCloud.SecurityCommandCenter
	handler := scc.New(scc.Options{
		MinSeverity:  string(cfg.MinSeverity),
		Organization: cfg.Organization,
	})
	return r.ingestAll(ctx, integ, handler, e)
}

// handleWiz routes a Wiz delivery through the source handler for whichever
// feed the payload shape identified. Like SCC there is no API call: the
// documented body template carries everything the finding needs.
func (r *Receiver) handleWiz(ctx context.Context, e webhook.Event) error {
	var (
		cap     capability
		handler func(*v1alpha1.Integration) source.Handler
	)
	switch e.Type {
	case wiz.EventIssue:
		cap = wizIssuesEnabled
		handler = func(integ *v1alpha1.Integration) source.Handler {
			return wiz.NewIssues(wiz.Options{MinSeverity: string(integ.Spec.Wiz.Issues.MinSeverity)})
		}
	case wiz.EventThreat:
		cap = wizDefendEnabled
		handler = func(integ *v1alpha1.Integration) source.Handler {
			return wiz.NewDefend(wiz.Options{MinSeverity: string(integ.Spec.Wiz.Defend.MinSeverity)})
		}
	default:
		return nil
	}
	integ, err := selectIntegration(ctx, r.Reader, r.Namespace, cap)
	if err != nil {
		if errors.Is(err, ErrNoIntegration) {
			return nil // this feed is not configured; nothing to ingest
		}
		return err
	}
	return r.ingestAll(ctx, integ, handler(integ), e)
}

// ingestAll normalizes one delivery through a source handler and folds every
// finding it yields into the cluster.
func (r *Receiver) ingestAll(
	ctx context.Context, integ *v1alpha1.Integration, h source.Handler, e webhook.Event,
) error {
	findings, err := h.Findings(ctx, e.Type, e.Payload)
	if err != nil {
		return fmt.Errorf("decode %s delivery: %w", h.ID(), err)
	}
	var errs []error
	for _, f := range findings {
		if err := r.Ingest.Ingest(ctx, integ, f); err != nil {
			errs = append(errs, fmt.Errorf("ingest %s %s: %w", h.ID(), alertLabel(f), err))
		}
	}
	return errors.Join(errs...)
}

// alertLabel names a finding for an error message, by whichever identifier
// its source uses.
func alertLabel(f source.Finding) string {
	if f.AlertID != "" {
		return f.AlertID
	}
	return fmt.Sprintf("%s alert %d", f.Repo, f.AlertNumber)
}

// alertGetter adapts Integration credentials to the ghas.AlertGetter seam.
type alertGetter struct {
	creds *Creds
	integ *v1alpha1.Integration
}

// GetAlert fetches the full alert with the Integration's client for the
// repository.
func (g *alertGetter) GetAlert(ctx context.Context, repo ghclient.Repo, number int) (*ghclient.Alert, error) {
	c, err := g.creds.Client(ctx, g.integ, repo)
	if err != nil {
		return nil, err
	}
	return c.GetAlert(ctx, repo, number)
}

func (r *Receiver) log() *slog.Logger {
	if r.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return r.Log
}
