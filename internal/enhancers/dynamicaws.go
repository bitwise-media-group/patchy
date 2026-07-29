// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package enhancers

import (
	"context"
	"fmt"
	"sync"

	"github.com/bitwise-media-group/patchy/internal/awsinv"
	"github.com/bitwise-media-group/patchy/pkg/enhance"
)

// AWSTagsConfig is one snapshot of the resourceTags capability, as read off
// the aws Integration at enhance time.
type AWSTagsConfig struct {
	// Backend selects and configures the inventory queried.
	Backend awsinv.Config
	// RepositoryHost is the forge host composed into resolved URLs; empty
	// means github.com.
	RepositoryHost string
	// Keys overrides the ownership tag names.
	Keys LabelKeys
}

// AWSConfigSource yields the current capability configuration: nil when the
// capability is off (the enhancer stands aside), an error when it cannot be
// read (the finding is held and retried rather than advanced without its
// enrichment).
type AWSConfigSource func(ctx context.Context) (*AWSTagsConfig, error)

// DynamicAWS is AWSTags with its configuration read from the Integration CR
// per enhancement instead of captured at process start. A config change — a
// different aggregator, a new view — takes effect on the next reconcile
// without a restart. Only the inventory client is stateful — building one
// verifies the backend remotely — so it is memoized by backend and swapped
// (closing the old one) when the backend changes.
type DynamicAWS struct {
	// Config reads the capability. Required.
	Config AWSConfigSource
	// NewInventory builds an inventory client for a backend; nil means
	// awsinv.New. The seam exists for tests, which must not dial AWS.
	NewInventory func(ctx context.Context, backend awsinv.Config) (AWSInventory, func() error, error)

	mu        sync.Mutex
	backend   string
	inventory AWSInventory
	close     func() error
}

var _ enhance.Enhancer = (*DynamicAWS)(nil)

// ID implements enhance.Enhancer. It deliberately reuses AWSTagsID: this is
// the same enhancer to everything downstream (enrichment attribution, sticky
// comments, attribute precedence), only its configuration plumbing differs.
func (*DynamicAWS) ID() string { return AWSTagsID }

// Enhance implements enhance.Enhancer.
func (d *DynamicAWS) Enhance(ctx context.Context, issue enhance.Issue) (*enhance.Enrichment, error) {
	cfg, err := d.Config(ctx)
	if err != nil {
		return nil, fmt.Errorf("aws-resource-tags: read integration config: %w", err)
	}
	if cfg == nil {
		return nil, nil // capability off: not ours
	}
	inventory, err := d.inventoryFor(ctx, cfg.Backend)
	if err != nil {
		return nil, fmt.Errorf("aws-resource-tags: %w", err)
	}
	inner, err := NewAWSTags(inventory, TagsOptions{
		Keys:        cfg.Keys,
		DefaultHost: cfg.RepositoryHost,
	})
	if err != nil {
		return nil, err
	}
	return inner.Enhance(ctx, issue)
}

// inventoryFor returns the memoized inventory client for the backend,
// building one on first use and swapping when the backend changes. An
// in-flight enhancement racing a swap may fail against the swapped client;
// that error is retryable by design, so nothing is lost.
func (d *DynamicAWS) inventoryFor(ctx context.Context, backend awsinv.Config) (AWSInventory, error) {
	key := backendKey(backend)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.inventory != nil && d.backend == key {
		return d.inventory, nil
	}
	build := d.NewInventory
	if build == nil {
		build = func(ctx context.Context, backend awsinv.Config) (AWSInventory, func() error, error) {
			c, err := awsinv.New(ctx, backend)
			if err != nil {
				return nil, nil, err
			}
			return c, c.Close, nil
		}
	}
	inventory, closer, err := build(ctx, backend)
	if err != nil {
		return nil, err
	}
	if d.close != nil {
		_ = d.close()
	}
	d.backend, d.inventory, d.close = key, inventory, closer
	return inventory, nil
}

// backendKey flattens a backend config into a comparable memo key — the
// config holds pointers, so comparing it directly would compare identity,
// not content.
func backendKey(c awsinv.Config) string {
	switch {
	case c.ConfigAggregator != nil:
		return "aggregator\x00" + c.ConfigAggregator.Name + "\x00" + c.ConfigAggregator.Region
	case c.ResourceExplorer != nil:
		return "view\x00" + c.ResourceExplorer.ViewARN
	default:
		return ""
	}
}

// Close releases the memoized client, for shutdown.
func (d *DynamicAWS) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.close == nil {
		return nil
	}
	err := d.close()
	d.backend, d.inventory, d.close = "", nil, nil
	return err
}
