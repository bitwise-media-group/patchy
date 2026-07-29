// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package enhancers

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/bitwise-media-group/patchy/internal/awsinv"
	"github.com/bitwise-media-group/patchy/pkg/enhance"
	"github.com/bitwise-media-group/patchy/pkg/source"
)

// AWSTagsID is the enhancer's id.
const AWSTagsID = "aws-resource-tags"

// maxTagAttributes bounds how many of a resource's tags become finding
// attributes, matching the cap the Wiz issue rendering applies. Finding
// status is a bounded surface; a resource carrying hundreds of tags does not
// get to fill it.
const maxTagAttributes = 24

// AWSInventory reads a cloud resource's tags. Declared here, next to its
// only consumer, so the enhancer's tests need no AWS client.
type AWSInventory interface {
	TagsFor(ctx context.Context, arn string) (*awsinv.Tags, error)
}

// AWSOptions configure the enhancer.
type AWSOptions struct {
	// Inventory looks resources up. Required.
	Inventory AWSInventory
	// Keys names the tags to read.
	Keys LabelKeys
	// DefaultHost is the forge host used when a resource names an org and a
	// repository but no host. Empty means github.com, matching the default
	// the Forge resolver already assumes.
	DefaultHost string
	// DefaultProvider is the forge family assumed when a resource names no
	// provider. Empty means github.
	DefaultProvider string
}

// AWSTags resolves a finding's repository from the ownership tags on the AWS
// resource it was raised against, and carries the resource's tags onto the
// finding as attributes.
//
// It answers only for AWS findings, and it answers only about the
// repository: ownership by person is a different question, and the CMDB
// enhancer is the one that knows it.
type AWSTags struct {
	inventory AWSInventory
	keys      LabelKeys
	host      string
	provider  string
}

var _ enhance.Enhancer = (*AWSTags)(nil)

// NewAWSTags builds the enhancer.
func NewAWSTags(o AWSOptions) (*AWSTags, error) {
	if o.Inventory == nil {
		return nil, errors.New("aws-resource-tags: an inventory client is required")
	}
	keys := LabelKeys{
		Org:      or(o.Keys.Org, DefaultOrgLabel),
		Name:     or(o.Keys.Name, DefaultNameLabel),
		Provider: or(o.Keys.Provider, DefaultProviderLabel),
		URL:      or(o.Keys.URL, DefaultURLLabel),
	}
	return &AWSTags{
		inventory: o.Inventory,
		keys:      keys,
		host:      or(o.DefaultHost, "github.com"),
		provider:  or(o.DefaultProvider, "github"),
	}, nil
}

// ID implements enhance.Enhancer.
func (*AWSTags) ID() string { return AWSTagsID }

// Enhance implements enhance.Enhancer.
//
// A resource that exists but carries no ownership tags returns an enrichment
// with no repository rather than an error: that is a final answer, and
// reporting it as a failure would hold the finding out of sight instead of
// handing it to a human. Only a lookup that might succeed next time errors.
func (a *AWSTags) Enhance(ctx context.Context, issue enhance.Issue) (*enhance.Enrichment, error) {
	cr := issue.CloudResource
	// The ARN-prefix guard skips the synthetic names a source falls back to
	// when a finding has no concrete resource (Wiz Defend's
	// "wiz-account:<id>"): no inventory records those.
	if cr == nil || !strings.EqualFold(cr.Provider, "aws") || !strings.HasPrefix(cr.Name, "arn:") {
		return nil, nil // not ours
	}

	tags, err := a.inventory.TagsFor(ctx, cr.Name)
	if err != nil {
		if errors.Is(err, awsinv.ErrNotFound) {
			// The resource is gone, unrecorded, or not indexed yet. Nothing
			// to wait for, but the finding still deserves the context it
			// arrived with.
			return &enhance.Enrichment{Attributes: awsAttributes(cr, nil)}, nil
		}
		return nil, fmt.Errorf("aws-resource-tags: %w", err)
	}

	return &enhance.Enrichment{
		Attributes: awsAttributes(cr, tags.Tags),
		Repository: repositoryFrom(tags.Tags, a.keys, a.provider, a.host),
	}, nil
}

// awsAttributes are the facts worth carrying onto the finding whether or not
// a repository resolved: the resource's identity, and its tags — which is
// what a human triaging a cloud finding needs in order to know where to
// look, and what this enhancer exists to fetch.
func awsAttributes(cr *source.CloudResource, tags map[string]string) map[string]string {
	attrs := map[string]string{}
	if cr.Project != "" {
		attrs["aws-account"] = cr.Project
	}
	if cr.Type != "" {
		attrs["resource-type"] = cr.Type
	}
	if cr.Location != "" {
		attrs["location"] = cr.Location
	}
	keys := slices.Sorted(maps.Keys(tags))
	if len(keys) > maxTagAttributes {
		keys = keys[:maxTagAttributes]
	}
	for _, k := range keys {
		attrs["tag:"+k] = tags[k]
	}
	if len(attrs) == 0 {
		return nil
	}
	return attrs
}
