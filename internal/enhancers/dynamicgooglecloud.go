// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package enhancers

import (
	"context"
	"fmt"
	"sync"

	"github.com/bitwise-media-group/patchy/internal/gcpasset"
	"github.com/bitwise-media-group/patchy/pkg/enhance"
)

// AssetConfig is one snapshot of the cloudAssetInventory capability, as read
// off the google-cloud Integration at enhance time.
type AssetConfig struct {
	// Scope bounds the asset search.
	Scope string
	// RepositoryHost is the forge host composed into resolved URLs; empty
	// means github.com.
	RepositoryHost string
	// Keys overrides the ownership label names.
	Keys LabelKeys
}

// ConfigSource yields the current capability configuration: nil when the
// capability is off (the enhancer stands aside), an error when it cannot be
// read (the finding is held and retried rather than advanced without its
// enrichment).
type ConfigSource func(ctx context.Context) (*AssetConfig, error)

// DynamicGoogleCloud is GoogleCloudLabels with its configuration read from
// the Integration CR per enhancement instead of captured at process start. A
// config change — a new scope, different label keys — takes effect on the
// next reconcile without a restart. Only the asset client is stateful: it
// holds a gRPC connection, so it is memoized by scope and swapped (closing
// the old one) when the scope changes.
type DynamicGoogleCloud struct {
	// Config reads the capability. Required.
	Config ConfigSource
	// NewAssets builds an asset client for a scope; nil means gcpasset.New.
	// The seam exists for tests, which must not dial Google.
	NewAssets func(ctx context.Context, scope string) (AssetLabels, func() error, error)

	mu     sync.Mutex
	scope  string
	assets AssetLabels
	close  func() error
}

var _ enhance.Enhancer = (*DynamicGoogleCloud)(nil)

// ID implements enhance.Enhancer. It deliberately reuses GoogleCloudLabelsID:
// this is the same enhancer to everything downstream (enrichment attribution,
// sticky comments, attribute precedence), only its configuration plumbing
// differs.
func (*DynamicGoogleCloud) ID() string { return GoogleCloudLabelsID }

// Enhance implements enhance.Enhancer.
func (d *DynamicGoogleCloud) Enhance(ctx context.Context, issue enhance.Issue) (*enhance.Enrichment, error) {
	cfg, err := d.Config(ctx)
	if err != nil {
		return nil, fmt.Errorf("google-cloud-labels: read integration config: %w", err)
	}
	if cfg == nil {
		return nil, nil // capability off: not ours
	}
	assets, err := d.assetsFor(ctx, cfg.Scope)
	if err != nil {
		return nil, fmt.Errorf("google-cloud-labels: %w", err)
	}
	inner, err := NewGoogleCloudLabels(GoogleCloudOptions{
		Assets:      assets,
		Keys:        cfg.Keys,
		DefaultHost: cfg.RepositoryHost,
	})
	if err != nil {
		return nil, err
	}
	return inner.Enhance(ctx, issue)
}

// assetsFor returns the memoized asset client for scope, building one on
// first use and swapping when the scope changes. An in-flight enhancement
// racing a swap may fail against the closed client; that error is retryable
// by design, so nothing is lost.
func (d *DynamicGoogleCloud) assetsFor(ctx context.Context, scope string) (AssetLabels, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.assets != nil && d.scope == scope {
		return d.assets, nil
	}
	build := d.NewAssets
	if build == nil {
		build = func(ctx context.Context, scope string) (AssetLabels, func() error, error) {
			c, err := gcpasset.New(ctx, scope)
			if err != nil {
				return nil, nil, err
			}
			return c, c.Close, nil
		}
	}
	assets, closer, err := build(ctx, scope)
	if err != nil {
		return nil, err
	}
	if d.close != nil {
		_ = d.close()
	}
	d.scope, d.assets, d.close = scope, assets, closer
	return assets, nil
}

// Close releases the memoized client, for shutdown.
func (d *DynamicGoogleCloud) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.close == nil {
		return nil
	}
	err := d.close()
	d.scope, d.assets, d.close = "", nil, nil
	return err
}
