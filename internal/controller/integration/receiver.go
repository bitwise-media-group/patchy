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
	"github.com/bitwise-media-group/patchy/internal/webhook"
)

// Receiver is the integration-controller's webhook.Handler: it turns
// validated GitHub deliveries into Finding writes. Scanner events go through
// the pkg/source handler seam into the Ingestor; tracking events (issues,
// comments, pull requests) go to the Signals handler.
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
	// Log receives receiver diagnostics; nil discards.
	Log *slog.Logger
}

// Secrets returns every configured github Integration's webhook secret — the
// candidate set for delivery validation (webhook.Config.SecretsFor).
func (r *Receiver) Secrets(ctx context.Context) [][]byte {
	var list v1alpha1.IntegrationList
	if err := r.Reader.List(ctx, &list, client.InNamespace(r.Namespace)); err != nil {
		r.log().LogAttrs(ctx, slog.LevelError, "list integrations for webhook secrets", slog.Any("error", err))
		return nil
	}
	var out [][]byte
	for i := range list.Items {
		integ := &list.Items[i]
		if integ.Spec.Provider != v1alpha1.IntegrationProviderGitHub || integ.Spec.Suspend {
			continue
		}
		secret, err := r.Creds.WebhookSecret(ctx, integ)
		if err != nil {
			r.log().LogAttrs(ctx, slog.LevelWarn, "integration webhook secret unavailable",
				slog.String("integration", integ.Name), slog.Any("error", err))
			continue
		}
		out = append(out, secret)
	}
	return out
}

// Handle implements webhook.Handler for the /github/webhooks path.
func (r *Receiver) Handle(ctx context.Context, e webhook.Event) error {
	switch e.Type {
	case "code_scanning_alert":
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
	handler := ghas.New(&alertGetter{creds: r.Creds, integ: integ})
	findings, err := handler.Findings(ctx, e.Type, e.Payload)
	if err != nil {
		return fmt.Errorf("decode scanner delivery: %w", err)
	}
	var errs []error
	for _, f := range findings {
		if err := r.Ingest.Ingest(ctx, integ, f); err != nil {
			errs = append(errs, fmt.Errorf("ingest %s alert %d: %w", f.Repo, f.AlertNumber, err))
		}
	}
	return errors.Join(errs...)
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
