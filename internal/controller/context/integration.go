// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package context

import (
	"context"
	"errors"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/bitwise-media-group/patchy/internal/awsinv"
	"github.com/bitwise-media-group/patchy/internal/enhancers"
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
