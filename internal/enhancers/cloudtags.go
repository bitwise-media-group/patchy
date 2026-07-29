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

	"github.com/bitwise-media-group/patchy/pkg/enhance"
	"github.com/bitwise-media-group/patchy/pkg/source"
)

// cloudTags is the shared engine of the tag-inventory enhancers (AWS, Azure):
// resolve a finding's repository from the ownership tags on the cloud
// resource it was raised against, and carry the resource's tags onto the
// finding as attributes. Each cloud contributes only its identity — the
// provider, the identifier shape, the inventory lookup — and this owns
// everything the clouds share, so the enhancers cannot drift apart.
//
// It answers only about the repository: ownership by person is a different
// question, and the CMDB enhancer is the one that knows it.
type cloudTags struct {
	// id is the enhancer's id, also its error-wrapping prefix.
	id string
	// cloud is the finding provider this enhancer owns.
	cloud string
	// idPrefix admits real resource identifiers ("arn:", "/") and skips the
	// synthetic names a source falls back to when a finding has no concrete
	// resource (Wiz Defend's "wiz-account:<id>"): no inventory records those.
	idPrefix string
	// accountKey names the attribute the resource's account or subscription
	// is carried under.
	accountKey string
	// final is the inventory's not-found sentinel — the one error that is a
	// clean answer rather than something to retry.
	final error
	// tagsFor looks the resource's tags up in the cloud inventory.
	tagsFor func(ctx context.Context, id string) (map[string]string, error)

	keys     LabelKeys
	host     string
	provider string
}

// ID implements enhance.Enhancer.
func (c *cloudTags) ID() string { return c.id }

// Enhance implements enhance.Enhancer.
//
// A resource that exists but carries no ownership tags returns an enrichment
// with no repository rather than an error: that is a final answer, and
// reporting it as a failure would hold the finding out of sight instead of
// handing it to a human. Only a lookup that might succeed next time errors.
func (c *cloudTags) Enhance(ctx context.Context, issue enhance.Issue) (*enhance.Enrichment, error) {
	cr := issue.CloudResource
	if cr == nil || !strings.EqualFold(cr.Provider, c.cloud) || !strings.HasPrefix(cr.Name, c.idPrefix) {
		return nil, nil // not ours
	}

	tags, err := c.tagsFor(ctx, cr.Name)
	if err != nil {
		if errors.Is(err, c.final) {
			// The resource is gone, unrecorded, or not indexed yet. Nothing
			// to wait for, but the finding still deserves the context it
			// arrived with.
			return &enhance.Enrichment{Attributes: c.attributes(cr, nil)}, nil
		}
		return nil, fmt.Errorf("%s: %w", c.id, err)
	}

	return &enhance.Enrichment{
		Attributes: c.attributes(cr, tags),
		Repository: repositoryFrom(tags, c.keys, c.provider, c.host),
	}, nil
}

// attributes are the facts worth carrying onto the finding whether or not a
// repository resolved: the resource's identity, and its tags — which is what
// a human triaging a cloud finding needs in order to know where to look, and
// what a tag-inventory enhancer exists to fetch.
func (c *cloudTags) attributes(cr *source.CloudResource, tags map[string]string) map[string]string {
	attrs := map[string]string{}
	if cr.Project != "" {
		attrs[c.accountKey] = cr.Project
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

// defaultKeys fills the conventional scm-repository-* names in for any label
// key an operator left unset.
func defaultKeys(k LabelKeys) LabelKeys {
	return LabelKeys{
		Org:      or(k.Org, DefaultOrgLabel),
		Name:     or(k.Name, DefaultNameLabel),
		Provider: or(k.Provider, DefaultProviderLabel),
		URL:      or(k.URL, DefaultURLLabel),
	}
}

// TagsOptions configure a tag-inventory enhancer; the vocabulary is shared
// across clouds, so the options are too (the inventory client itself is the
// per-cloud part, passed beside them).
type TagsOptions struct {
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

// cloudTagsSpec is a cloud's identity, as the per-cloud constructors spell
// it.
type cloudTagsSpec struct {
	id, cloud, idPrefix, accountKey string
	final                           error
}

// newCloudTags builds the engine over a typed inventory lookup — each cloud
// client answers with its own Tags struct, adapted here — and applies the
// shared defaults.
func newCloudTags[T any](
	spec cloudTagsSpec,
	lookup func(ctx context.Context, id string) (*T, error),
	tags func(*T) map[string]string,
	o TagsOptions,
) cloudTags {
	return cloudTags{
		id:         spec.id,
		cloud:      spec.cloud,
		idPrefix:   spec.idPrefix,
		accountKey: spec.accountKey,
		final:      spec.final,
		tagsFor: func(ctx context.Context, id string) (map[string]string, error) {
			t, err := lookup(ctx, id)
			if err != nil {
				return nil, err
			}
			return tags(t), nil
		},
		keys:     defaultKeys(o.Keys),
		host:     or(o.DefaultHost, "github.com"),
		provider: or(o.DefaultProvider, "github"),
	}
}
