// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package enhancers

import (
	"context"
	"errors"

	"github.com/bitwise-media-group/patchy/internal/awsinv"
	"github.com/bitwise-media-group/patchy/pkg/enhance"
)

// AWSTagsID is the enhancer's id.
const AWSTagsID = "aws-resource-tags"

// AWSInventory reads a cloud resource's tags. Declared here, next to its
// only consumer, so the enhancer's tests need no AWS client.
type AWSInventory interface {
	TagsFor(ctx context.Context, arn string) (*awsinv.Tags, error)
}

// AWSTags is the cloudTags engine configured for AWS: ARN-shaped
// identifiers, awsinv's not-found sentinel, the aws-account attribute.
type AWSTags struct{ cloudTags }

var _ enhance.Enhancer = (*AWSTags)(nil)

// NewAWSTags builds the enhancer.
func NewAWSTags(inventory AWSInventory, o TagsOptions) (*AWSTags, error) {
	if inventory == nil {
		return nil, errors.New("aws-resource-tags: an inventory client is required")
	}
	return &AWSTags{newCloudTags(
		cloudTagsSpec{
			id: AWSTagsID, cloud: "aws", idPrefix: "arn:",
			accountKey: "aws-account", final: awsinv.ErrNotFound,
		},
		inventory.TagsFor,
		func(t *awsinv.Tags) map[string]string { return t.Tags },
		o,
	)}, nil
}
