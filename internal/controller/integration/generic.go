// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package integration

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/generic"
	"github.com/bitwise-media-group/patchy/internal/ghas"
	"github.com/bitwise-media-group/patchy/internal/scc"
	"github.com/bitwise-media-group/patchy/internal/webhook"
	"github.com/bitwise-media-group/patchy/internal/wiz"
)

// GenericPathPattern is the wildcard route generic integrations deliver to.
// The pattern itself lives with the contract helpers in internal/generic (a
// k8s-free package the CLI dev harness also hosts it from); the /generic/
// prefix keeps the ingress path list static and keeps integration names out
// of the provider routes' namespace.
const GenericPathPattern = generic.PathPattern

// GenericPathFor is the concrete webhook path of one generic Integration,
// advertised on status.webhookPath.
func GenericPathFor(name string) string { return generic.PathFor(name) }

// reservedSourceIDs are the built-in source ids a generic Integration's name
// must not shadow: the name becomes its finding source id, and a collision
// would corrupt accumulation keys and misroute verdict write-back. Guarded
// by CEL on the CRD; re-checked here so the Ready condition tells the
// operator even if the CRD predates the rule.
var reservedSourceIDs = map[string]bool{
	ghas.ID:      true,
	scc.ID:       true,
	wiz.IssuesID: true,
	wiz.DefendID: true,
}

// genericDecoder labels a generic delivery: the event type from the payload
// envelope, the delivery id from the caller's header else a body digest.
func genericDecoder(r *http.Request, body []byte) (eventType, deliveryID string, err error) {
	event, err := generic.Detect(body)
	if err != nil {
		return "", "", err
	}
	return event, generic.DeliveryID(r, body), nil
}

// genericSecretsFor returns the candidate secret for a delivery on the
// wildcard route: the webhook secret of the Integration the path names, and
// only that one — integration A's secret must never validate a delivery
// addressed to B. An unknown name, a disabled source, or an unreadable
// Secret all yield an empty set, which fails authentication without leaking
// which of those it was.
func (r *Receiver) genericSecretsFor(ctx context.Context, req *http.Request) [][]byte {
	integ, err := r.genericIntegration(ctx, req.PathValue("name"))
	if err != nil || integ == nil {
		if err != nil {
			r.log().LogAttrs(ctx, slog.LevelWarn, "generic integration lookup failed",
				slog.String("integration", req.PathValue("name")), slog.Any("error", err))
		}
		return nil
	}
	secret, err := r.Creds.WebhookSecret(ctx, integ)
	if err != nil {
		r.log().LogAttrs(ctx, slog.LevelWarn, "generic integration webhook credential unavailable",
			slog.String("integration", integ.Name), slog.Any("error", err))
		return nil
	}
	return [][]byte{secret}
}

// handleGeneric routes one validated generic delivery through the named
// Integration's source handler. The capability is re-checked here — the auth
// hook already did, but the Integration can change between the 202 and the
// worker picking the delivery up.
func (r *Receiver) handleGeneric(ctx context.Context, e webhook.Event) error {
	name, ok := generic.NameFromPath(e.Path)
	if !ok {
		return fmt.Errorf("generic delivery on unparseable path %s", e.Path)
	}
	integ, err := r.genericIntegration(ctx, name)
	if err != nil {
		return err
	}
	if integ == nil {
		return nil // gone or disabled since authentication; nothing to ingest
	}
	cfg := integ.Spec.Generic.Source
	handler := generic.NewSource(integ.Name, generic.Options{MinSeverity: string(cfg.MinSeverity)})
	return r.ingestAll(ctx, integ, handler, e)
}

// genericIntegration fetches the named Integration when it is an enabled
// generic source: (nil, nil) when it does not exist, is not generic, is
// suspended, or has the source capability off — the caller treats all four
// the same, and an attacker probing the route cannot tell them apart.
func (r *Receiver) genericIntegration(ctx context.Context, name string) (*v1alpha1.Integration, error) {
	if name == "" {
		return nil, nil
	}
	var integ v1alpha1.Integration
	key := types.NamespacedName{Namespace: r.Namespace, Name: name}
	if err := r.Reader.Get(ctx, key, &integ); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get generic integration %s: %w", key, err)
	}
	if !genericSourceEnabled(&integ) {
		return nil, nil
	}
	return &integ, nil
}

// validateGeneric checks the generic Integration is usable: at least one
// capability on, the shared HMAC secret present, and a name safe to use as a
// source id and label value.
func (c *Creds) validateGeneric(ctx context.Context, integ *v1alpha1.Integration) error {
	g := integ.Spec.Generic
	sourceOn := g != nil && g.Source != nil && g.Source.Enabled
	enhanceOn := g != nil && g.Enhance != nil && g.Enhance.Enabled
	if !sourceOn && !enhanceOn {
		return errors.New("generic integration enables no capability")
	}
	if reservedSourceIDs[integ.Name] {
		return fmt.Errorf("integration name %s is a reserved source id", integ.Name)
	}
	// The name is stamped into the security-source label; a label value
	// caps at 63 characters where a resource name does not.
	if len(integ.Name) > 63 {
		return fmt.Errorf("integration name %s exceeds 63 characters, the label-value limit", integ.Name)
	}
	if sourceOn && g.Source.Resolver != nil && g.Source.Resolver.Enabled && g.Source.Resolver.URL == "" {
		return errors.New("generic resolver is enabled without a url")
	}
	_, err := c.WebhookSecret(ctx, integ)
	return err
}

// genericSourceEnabled reports whether the Integration receives generic
// findings deliveries.
func genericSourceEnabled(i *v1alpha1.Integration) bool {
	return !i.Spec.Suspend && i.Spec.Provider == v1alpha1.IntegrationProviderGeneric &&
		i.Spec.Generic != nil && i.Spec.Generic.Source != nil && i.Spec.Generic.Source.Enabled
}

// genericResolverEnabled reports whether the Integration receives verdict
// write-back calls.
func genericResolverEnabled(i *v1alpha1.Integration) bool {
	return genericSourceEnabled(i) &&
		i.Spec.Generic.Source.Resolver != nil && i.Spec.Generic.Source.Resolver.Enabled
}

// webhookPath is what status.webhookPath advertises: the per-provider route,
// except a generic Integration, which has its own per-name path.
func webhookPath(integ *v1alpha1.Integration) string {
	if integ.Spec.Provider == v1alpha1.IntegrationProviderGeneric {
		return GenericPathFor(integ.Name)
	}
	return "/" + string(integ.Spec.Provider) + "/webhooks"
}
