// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package enhancers

import (
	"context"
	"errors"

	"github.com/bitwise-media-group/patchy/internal/azureinv"
	"github.com/bitwise-media-group/patchy/pkg/enhance"
)

// AzureTagsID is the enhancer's id.
const AzureTagsID = "azure-resource-tags"

// AzureInventory reads a cloud resource's tags. Declared here, next to its
// only consumer, so the enhancer's tests need no Azure client.
type AzureInventory interface {
	TagsFor(ctx context.Context, id string) (*azureinv.Tags, error)
}

// AzureTags is the cloudTags engine configured for Azure: ARM resource IDs
// (the "/" prefix admits /subscriptions/... and the tenant-level
// /providers/...), azureinv's not-found sentinel, the azure-subscription
// attribute.
type AzureTags struct{ cloudTags }

var _ enhance.Enhancer = (*AzureTags)(nil)

// NewAzureTags builds the enhancer.
func NewAzureTags(inventory AzureInventory, o TagsOptions) (*AzureTags, error) {
	if inventory == nil {
		return nil, errors.New("azure-resource-tags: an inventory client is required")
	}
	return &AzureTags{newCloudTags(
		cloudTagsSpec{
			id: AzureTagsID, cloud: "azure", idPrefix: "/",
			accountKey: "azure-subscription", final: azureinv.ErrNotFound,
		},
		inventory.TagsFor,
		func(t *azureinv.Tags) map[string]string { return t.Tags },
		o,
	)}, nil
}
