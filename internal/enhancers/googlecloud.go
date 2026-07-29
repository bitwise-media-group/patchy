// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package enhancers

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/bitwise-media-group/patchy/internal/gcpasset"
	"github.com/bitwise-media-group/patchy/pkg/enhance"
	"github.com/bitwise-media-group/patchy/pkg/source"
)

// Default label keys. A cloud resource says who owns it by carrying these;
// they are the bridge between an infrastructure finding and the repository
// whose code provisions the thing that is wrong.
//
// Google Cloud label values are lowercase alphanumerics, hyphens and
// underscores, capped at 63 characters — which a URL cannot survive. Hence
// the triple: an org and a name both fit comfortably, and the URL form is
// there for security marks, whose values are far less constrained.
const (
	DefaultOrgLabel      = "scm-repository-org"
	DefaultNameLabel     = "scm-repository-name"
	DefaultProviderLabel = "scm-repository-provider"
	DefaultURLLabel      = "scm-repository-url"
)

// GoogleCloudLabelsID is the enhancer's id.
const GoogleCloudLabelsID = "google-cloud-labels"

// AssetLabels reads a cloud resource's labels. Declared here, next to its
// only consumer, so the enhancer's tests need no Google Cloud client. The
// display name rides along for the lookup's fallback: an identifier a source
// could not spell the Asset Inventory way still has a second chance.
type AssetLabels interface {
	LabelsFor(ctx context.Context, resourceName, displayName string) (*gcpasset.Labels, error)
}

// LabelKeys names the labels the enhancer reads. Zero values fall back to the
// defaults, so an operator overrides only what differs from the convention.
type LabelKeys struct {
	Org      string
	Name     string
	Provider string
	URL      string
}

// GoogleCloudOptions configure the enhancer.
type GoogleCloudOptions struct {
	// Assets looks resources up. Required.
	Assets AssetLabels
	// Keys names the labels to read.
	Keys LabelKeys
	// DefaultHost is the forge host used when a resource names an org and a
	// repository but no host. Empty means github.com, matching the default
	// the Forge resolver already assumes.
	DefaultHost string
	// DefaultProvider is the forge family assumed when a resource names no
	// provider. Empty means github.
	DefaultProvider string
}

// GoogleCloudLabels resolves a finding's repository from the ownership labels
// on the Google Cloud resource it was raised against.
//
// It answers only for Google Cloud findings, and it answers only about the
// repository: ownership by person is a different question, and the CMDB
// enhancer is the one that knows it.
type GoogleCloudLabels struct {
	assets   AssetLabels
	keys     LabelKeys
	host     string
	provider string
}

var _ enhance.Enhancer = (*GoogleCloudLabels)(nil)

// NewGoogleCloudLabels builds the enhancer.
func NewGoogleCloudLabels(o GoogleCloudOptions) (*GoogleCloudLabels, error) {
	if o.Assets == nil {
		return nil, errors.New("google-cloud-labels: an asset client is required")
	}
	return &GoogleCloudLabels{
		assets:   o.Assets,
		keys:     defaultKeys(o.Keys),
		host:     or(o.DefaultHost, "github.com"),
		provider: or(o.DefaultProvider, "github"),
	}, nil
}

// ID implements enhance.Enhancer.
func (*GoogleCloudLabels) ID() string { return GoogleCloudLabelsID }

// Enhance implements enhance.Enhancer.
//
// A resource that exists but carries no ownership labels returns an
// enrichment with no repository rather than an error: that is a final answer,
// and reporting it as a failure would hold the finding out of sight instead
// of handing it to a human. Only a lookup that might succeed next time
// errors.
func (g *GoogleCloudLabels) Enhance(ctx context.Context, issue enhance.Issue) (*enhance.Enrichment, error) {
	cr := issue.CloudResource
	if cr == nil || !strings.EqualFold(cr.Provider, "google") {
		return nil, nil // not ours
	}

	labels, err := g.assets.LabelsFor(ctx, cr.Name, cr.DisplayName)
	if err != nil {
		if errors.Is(err, gcpasset.ErrNotFound) {
			// The resource is gone or out of scope. Nothing to wait for, but
			// the finding still deserves the context it arrived with.
			return &enhance.Enrichment{Attributes: attributes(cr)}, nil
		}
		return nil, fmt.Errorf("google-cloud-labels: %w", err)
	}

	return &enhance.Enrichment{
		Attributes: attributes(cr),
		Repository: repositoryFrom(labels.Labels, g.keys, g.provider, g.host),
	}, nil
}

// attributes are the facts worth carrying onto the finding whether or not a
// repository resolved: they are what a human triaging a cloud finding needs
// in order to know where to look.
func attributes(cr *source.CloudResource) map[string]string {
	attrs := map[string]string{}
	if p := strings.TrimPrefix(cr.Project, "projects/"); p != "" {
		attrs["gcp-project"] = p
	}
	if cr.Type != "" {
		attrs["resource-type"] = cr.Type
	}
	if cr.Location != "" {
		attrs["location"] = cr.Location
	}
	if len(attrs) == 0 {
		return nil
	}
	return attrs
}
