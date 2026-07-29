// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package enhancers

import (
	"context"
	"fmt"
	"sync"

	"github.com/bitwise-media-group/patchy/internal/azureinv"
	"github.com/bitwise-media-group/patchy/pkg/enhance"
)

// AzureTagsConfig is one snapshot of the resourceTags capability, as read off
// the azure Integration at enhance time.
type AzureTagsConfig struct {
	// ManagementGroup narrows the Resource Graph scope; empty means every
	// subscription the ambient identity can read.
	ManagementGroup string
	// RepositoryHost is the forge host composed into resolved URLs; empty
	// means github.com.
	RepositoryHost string
	// Keys overrides the ownership tag names.
	Keys LabelKeys
}

// AzureConfigSource yields the current capability configuration: nil when the
// capability is off (the enhancer stands aside), an error when it cannot be
// read (the finding is held and retried rather than advanced without its
// enrichment).
type AzureConfigSource func(ctx context.Context) (*AzureTagsConfig, error)

// DynamicAzure is AzureTags with its configuration read from the Integration
// CR per enhancement instead of captured at process start. A config change —
// a different management group — takes effect on the next reconcile without a
// restart. Only the inventory client is stateful — building one verifies the
// scope remotely — so it is memoized by management group and swapped (closing
// the old one) when the scope changes.
type DynamicAzure struct {
	// Config reads the capability. Required.
	Config AzureConfigSource
	// NewInventory builds an inventory client for a scope; nil means
	// azureinv.New. The seam exists for tests, which must not dial Azure.
	NewInventory func(ctx context.Context, cfg azureinv.Config) (AzureInventory, func() error, error)

	mu              sync.Mutex
	managementGroup string
	inventory       AzureInventory
	close           func() error
}

var _ enhance.Enhancer = (*DynamicAzure)(nil)

// ID implements enhance.Enhancer. It deliberately reuses AzureTagsID: this is
// the same enhancer to everything downstream (enrichment attribution, sticky
// comments, attribute precedence), only its configuration plumbing differs.
func (*DynamicAzure) ID() string { return AzureTagsID }

// Enhance implements enhance.Enhancer.
func (d *DynamicAzure) Enhance(ctx context.Context, issue enhance.Issue) (*enhance.Enrichment, error) {
	cfg, err := d.Config(ctx)
	if err != nil {
		return nil, fmt.Errorf("azure-resource-tags: read integration config: %w", err)
	}
	if cfg == nil {
		return nil, nil // capability off: not ours
	}
	inventory, err := d.inventoryFor(ctx, cfg.ManagementGroup)
	if err != nil {
		return nil, fmt.Errorf("azure-resource-tags: %w", err)
	}
	inner, err := NewAzureTags(inventory, TagsOptions{
		Keys:        cfg.Keys,
		DefaultHost: cfg.RepositoryHost,
	})
	if err != nil {
		return nil, err
	}
	return inner.Enhance(ctx, issue)
}

// inventoryFor returns the memoized inventory client for the management
// group, building one on first use and swapping when the scope changes. The
// group string is its own memo key — no pointers to flatten. An in-flight
// enhancement racing a swap may fail against the swapped client; that error
// is retryable by design, so nothing is lost.
func (d *DynamicAzure) inventoryFor(ctx context.Context, managementGroup string) (AzureInventory, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.inventory != nil && d.managementGroup == managementGroup {
		return d.inventory, nil
	}
	build := d.NewInventory
	if build == nil {
		build = func(ctx context.Context, cfg azureinv.Config) (AzureInventory, func() error, error) {
			c, err := azureinv.New(ctx, cfg)
			if err != nil {
				return nil, nil, err
			}
			return c, c.Close, nil
		}
	}
	inventory, closer, err := build(ctx, azureinv.Config{ManagementGroup: managementGroup})
	if err != nil {
		return nil, err
	}
	if d.close != nil {
		_ = d.close()
	}
	d.managementGroup, d.inventory, d.close = managementGroup, inventory, closer
	return inventory, nil
}

// Close releases the memoized client, for shutdown.
func (d *DynamicAzure) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.close == nil {
		return nil
	}
	err := d.close()
	d.managementGroup, d.inventory, d.close = "", nil, nil
	return err
}
