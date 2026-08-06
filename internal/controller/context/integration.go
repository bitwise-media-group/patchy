// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package context

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/awsinv"
	"github.com/bitwise-media-group/patchy/internal/enhancers"
	"github.com/bitwise-media-group/patchy/internal/ghsecret"
	"github.com/bitwise-media-group/patchy/internal/integrationcap"
)

// AssetConfigSource reads the cloudAssetInventory capability off the
// namespace's google-cloud Integration, through the manager's informer cache.
// Read per enhancement rather than captured at startup, so an operator
// changing the Integration needs no controller restart — the same rule the
// receiver applies to webhook secrets.
//
// No capability configured is a nil config (the enhancer stands aside); two
// Integrations claiming it is an error (the singleton rule), which fails the
// enhancement closed into a hold rather than picking one arbitrarily.
func AssetConfigSource(r client.Reader, namespace string) enhancers.ConfigSource {
	return func(ctx context.Context) (*enhancers.AssetConfig, error) {
		integ, err := integrationcap.Select(ctx, r, namespace, integrationcap.CloudAssetInventoryEnabled)
		if err != nil {
			if errors.Is(err, integrationcap.ErrNoIntegration) {
				return nil, nil
			}
			return nil, err
		}
		cai := integ.Spec.GoogleCloud.CloudAssetInventory
		cfg := &enhancers.AssetConfig{
			Scope:          cai.Scope,
			RepositoryHost: cai.RepositoryHost,
		}
		if l := cai.Labels; l != nil {
			cfg.Keys = enhancers.LabelKeys{Org: l.Org, Name: l.Name, Provider: l.Provider, URL: l.URL}
		}
		return cfg, nil
	}
}

// AWSTagsConfigSource reads the resourceTags capability off the namespace's
// aws Integration, under the same rules as AssetConfigSource: per
// enhancement through the informer cache, nil when unconfigured, an error on
// ambiguity.
func AWSTagsConfigSource(r client.Reader, namespace string) enhancers.AWSConfigSource {
	return func(ctx context.Context) (*enhancers.AWSTagsConfig, error) {
		integ, err := integrationcap.Select(ctx, r, namespace, integrationcap.AWSResourceTagsEnabled)
		if err != nil {
			if errors.Is(err, integrationcap.ErrNoIntegration) {
				return nil, nil
			}
			return nil, err
		}
		rt := integ.Spec.AWS.ResourceTags
		cfg := &enhancers.AWSTagsConfig{RepositoryHost: rt.RepositoryHost}
		if a := rt.ConfigAggregator; a != nil {
			cfg.Backend.ConfigAggregator = &awsinv.ConfigAggregator{Name: a.Name, Region: a.Region}
		}
		if e := rt.ResourceExplorer; e != nil {
			cfg.Backend.ResourceExplorer = &awsinv.ResourceExplorer{ViewARN: e.ViewARN}
		}
		if l := rt.Tags; l != nil {
			cfg.Keys = enhancers.LabelKeys{Org: l.Org, Name: l.Name, Provider: l.Provider, URL: l.URL}
		}
		return cfg, nil
	}
}

// GenericEnhancerConfigSource reads every enabled generic enhancer endpoint
// in the namespace: the Integration list through the informer cache, each
// signing secret through the API reader (Secrets are never cached, the
// cluster-wide rule). Generic is the one capability exempt from the
// singleton rule, so this Lists rather than Selects — every enabled
// integration enhances, each under its own name.
//
// A Secret that cannot be read does not drop its integration from the list:
// the config is returned secret-less, the call fails for that endpoint
// alone, and the per-endpoint error carries the integration's name — one
// broken Secret must not silently skip an enhancer, nor take down the rest.
func GenericEnhancerConfigSource(
	r client.Reader, secrets client.Reader, namespace string, log *slog.Logger,
) enhancers.GenericConfigSource {
	return func(ctx context.Context) ([]enhancers.GenericConfig, error) {
		var list v1alpha1.IntegrationList
		if err := r.List(ctx, &list, client.InNamespace(namespace)); err != nil {
			return nil, fmt.Errorf("list integrations: %w", err)
		}
		var out []enhancers.GenericConfig
		for i := range list.Items {
			integ := &list.Items[i]
			if !integrationcap.GenericEnhanceEnabled(integ) {
				continue
			}
			cfg := enhancers.GenericConfig{
				Name:    integ.Name,
				URL:     integ.Spec.Generic.Enhance.URL,
				Timeout: integ.Spec.Generic.Enhance.Timeout.Duration,
			}
			cfg.Secret = webhookSecret(ctx, secrets, integ, log)
			out = append(out, cfg)
		}
		return out, nil
	}
}

// webhookSecret reads one integration's signing secret, nil when it cannot
// be read (logged; the endpoint's own call will fail with attribution).
func webhookSecret(ctx context.Context, r client.Reader, integ *v1alpha1.Integration, log *slog.Logger) []byte {
	if integ.Spec.SecretRef == nil {
		return nil
	}
	var secret corev1.Secret
	key := types.NamespacedName{Namespace: integ.Namespace, Name: integ.Spec.SecretRef.Name}
	if err := r.Get(ctx, key, &secret); err != nil {
		if log != nil {
			log.LogAttrs(ctx, slog.LevelWarn, "generic enhancer secret unavailable",
				slog.String("integration", integ.Name), slog.Any("error", err))
		}
		return nil
	}
	return secret.Data[ghsecret.KeyWebhookSecret]
}

// AzureTagsConfigSource reads the resourceTags capability off the namespace's
// azure Integration, under the same rules as AssetConfigSource: per
// enhancement through the informer cache, nil when unconfigured, an error on
// ambiguity.
func AzureTagsConfigSource(r client.Reader, namespace string) enhancers.AzureConfigSource {
	return func(ctx context.Context) (*enhancers.AzureTagsConfig, error) {
		integ, err := integrationcap.Select(ctx, r, namespace, integrationcap.AzureResourceTagsEnabled)
		if err != nil {
			if errors.Is(err, integrationcap.ErrNoIntegration) {
				return nil, nil
			}
			return nil, err
		}
		rt := integ.Spec.Azure.ResourceTags
		cfg := &enhancers.AzureTagsConfig{
			ManagementGroup: rt.ManagementGroup,
			RepositoryHost:  rt.RepositoryHost,
		}
		if l := rt.Tags; l != nil {
			cfg.Keys = enhancers.LabelKeys{Org: l.Org, Name: l.Name, Provider: l.Provider, URL: l.URL}
		}
		return cfg, nil
	}
}
