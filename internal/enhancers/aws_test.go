// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package enhancers

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/bitwise-media-group/patchy/internal/awsinv"
	"github.com/bitwise-media-group/patchy/pkg/enhance"
	"github.com/bitwise-media-group/patchy/pkg/source"
)

// fakeInventory stands in for the AWS inventory.
type fakeInventory struct {
	tags  map[string]string
	err   error
	calls int
}

func (f *fakeInventory) TagsFor(_ context.Context, arn string) (*awsinv.Tags, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return &awsinv.Tags{Name: arn, Tags: f.tags}, nil
}

// s3bucket is a Wiz finding's AWS cloud resource.
func s3bucket() *source.CloudResource {
	return &source.CloudResource{
		Provider: "aws",
		Name:     "arn:aws:s3:::acme-legacy-assets",
		Type:     "AWS::S3::Bucket",
		Project:  "123456789012",
		Location: "eu-west-1",
	}
}

func newAWSEnhancer(t *testing.T, inventory AWSInventory, keys LabelKeys) *AWSTags {
	t.Helper()
	e, err := NewAWSTags(AWSOptions{Inventory: inventory, Keys: keys})
	if err != nil {
		t.Fatalf("NewAWSTags: %v", err)
	}
	return e
}

func TestAWSTagsResolvesRepository(t *testing.T) {
	inventory := &fakeInventory{tags: map[string]string{
		"scm-repository-org":  "acme",
		"scm-repository-name": "infra-prod",
		"owner":               "platform",
	}}
	got, err := newAWSEnhancer(t, inventory, LabelKeys{}).
		Enhance(t.Context(), enhance.Issue{CloudResource: s3bucket()})
	if err != nil {
		t.Fatalf("Enhance() = %v, want nil", err)
	}
	want := &source.RepositoryRef{
		Provider: "github", Owner: "acme", Name: "infra-prod",
		URL: "https://github.com/acme/infra-prod",
	}
	if !reflect.DeepEqual(got.Repository, want) {
		t.Errorf("Repository = %+v, want %+v", got.Repository, want)
	}
	// The context is carried whether or not a repository resolved — and the
	// tags themselves are the enrichment.
	wantAttrs := map[string]string{
		"aws-account": "123456789012", "resource-type": "AWS::S3::Bucket",
		"location":  "eu-west-1",
		"tag:owner": "platform", "tag:scm-repository-org": "acme",
		"tag:scm-repository-name": "infra-prod",
	}
	if !reflect.DeepEqual(got.Attributes, wantAttrs) {
		t.Errorf("Attributes = %+v, want %+v", got.Attributes, wantAttrs)
	}
}

// A URL is the only form that can name a self-hosted forge, so a resource
// carrying one means it deliberately — and an AWS tag value can carry the
// scheme verbatim.
func TestAWSTagsURLSupersedesTriple(t *testing.T) {
	inventory := &fakeInventory{tags: map[string]string{
		"scm-repository-org":  "acme",
		"scm-repository-name": "infra-prod",
		"scm-repository-url":  "https://ghe.acme.internal/platform/infra",
	}}
	got, err := newAWSEnhancer(t, inventory, LabelKeys{}).
		Enhance(t.Context(), enhance.Issue{CloudResource: s3bucket()})
	if err != nil {
		t.Fatalf("Enhance() = %v, want nil", err)
	}
	if got.Repository.URL != "https://ghe.acme.internal/platform/infra" {
		t.Errorf("URL = %q, want the explicit one", got.Repository.URL)
	}
}

func TestAWSTagsHonoursCustomKeys(t *testing.T) {
	inventory := &fakeInventory{tags: map[string]string{
		"owner-org":  "acme",
		"owner-repo": "infra-prod",
	}}
	keys := LabelKeys{Org: "owner-org", Name: "owner-repo"}
	got, err := newAWSEnhancer(t, inventory, keys).
		Enhance(t.Context(), enhance.Issue{CloudResource: s3bucket()})
	if err != nil {
		t.Fatalf("Enhance() = %v, want nil", err)
	}
	if got.Repository == nil || got.Repository.Name != "infra-prod" {
		t.Errorf("Repository = %+v, want the custom keys honoured", got.Repository)
	}
}

// A resource that exists but carries no ownership tags is a final answer,
// not a failure: reporting it as an error would hold the finding out of
// sight instead of handing it to a human.
func TestAWSTagsUntaggedResourceIsNotAnError(t *testing.T) {
	for _, tt := range []struct {
		name string
		tags map[string]string
	}{
		{"no tags at all", nil},
		{"tags but none of ours", map[string]string{"env": "prod"}},
		{"an org with no name", map[string]string{"scm-repository-org": "acme"}},
		{"a name with no org", map[string]string{"scm-repository-name": "infra"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := newAWSEnhancer(t, &fakeInventory{tags: tt.tags}, LabelKeys{}).
				Enhance(t.Context(), enhance.Issue{CloudResource: s3bucket()})
			if err != nil {
				t.Fatalf("Enhance() = %v, want nil", err)
			}
			if got == nil || got.Repository != nil {
				t.Errorf("Repository = %+v, want nil with no error", got)
			}
		})
	}
}

// A deleted or unrecorded resource will not appear next time either, so it
// is reported as a clean no-answer rather than something to retry.
func TestAWSTagsMissingResourceIsFinal(t *testing.T) {
	inventory := &fakeInventory{err: awsinv.ErrNotFound}
	got, err := newAWSEnhancer(t, inventory, LabelKeys{}).
		Enhance(t.Context(), enhance.Issue{CloudResource: s3bucket()})
	if err != nil {
		t.Fatalf("Enhance() = %v, want nil: a missing resource is not retryable", err)
	}
	if got.Repository != nil {
		t.Errorf("Repository = %+v, want nil", got.Repository)
	}
	// The resource's own context still travels, even unresolved.
	if got.Attributes["aws-account"] != "123456789012" {
		t.Errorf("Attributes = %+v, want the finding's context regardless", got.Attributes)
	}
}

// Anything else might succeed next time, so it errors — which is what makes
// the context-controller hold the finding rather than advance it.
func TestAWSTagsTransientFailureErrors(t *testing.T) {
	inventory := &fakeInventory{err: errors.New("deadline exceeded")}
	if _, err := newAWSEnhancer(t, inventory, LabelKeys{}).
		Enhance(t.Context(), enhance.Issue{CloudResource: s3bucket()}); err == nil {
		t.Error("Enhance() = nil, want an error so the finding is retried")
	}
}

func TestAWSTagsSkipsFindingsItDoesNotOwn(t *testing.T) {
	for _, tt := range []struct {
		name  string
		issue enhance.Issue
	}{
		{"a code finding", enhance.Issue{Repo: source.Repo{Owner: "acme", Name: "shop"}}},
		{
			"another cloud platform",
			enhance.Issue{CloudResource: bucket()},
		},
		{
			// Wiz Defend falls back to a synthetic account pseudo-resource
			// when a threat names no concrete one; no inventory records it.
			"a synthetic account resource",
			enhance.Issue{CloudResource: &source.CloudResource{
				Provider: "aws", Name: "wiz-account:123456789012",
			}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			inventory := &fakeInventory{}
			got, err := newAWSEnhancer(t, inventory, LabelKeys{}).Enhance(t.Context(), tt.issue)
			if err != nil || got != nil {
				t.Errorf("Enhance() = (%+v, %v), want (nil, nil)", got, err)
			}
			if inventory.calls != 0 {
				t.Errorf("looked up %d resources, want none", inventory.calls)
			}
		})
	}
}

// Finding status is a bounded surface: the tag attributes are capped, and
// deterministically — sorted by key — so retries do not flap the status.
func TestAWSTagsCapsTagAttributes(t *testing.T) {
	tags := map[string]string{}
	for i := range 40 {
		tags[fmt.Sprintf("tag-%02d", i)] = "v"
	}
	got, err := newAWSEnhancer(t, &fakeInventory{tags: tags}, LabelKeys{}).
		Enhance(t.Context(), enhance.Issue{CloudResource: s3bucket()})
	if err != nil {
		t.Fatalf("Enhance() = %v, want nil", err)
	}
	var count int
	for k := range got.Attributes {
		if len(k) > 4 && k[:4] == "tag:" {
			count++
		}
	}
	if count != maxTagAttributes {
		t.Errorf("kept %d tag attributes, want %d", count, maxTagAttributes)
	}
	if _, ok := got.Attributes["tag:tag-00"]; !ok {
		t.Error("the cap must keep the first keys in sorted order")
	}
	if _, ok := got.Attributes["tag:tag-39"]; ok {
		t.Error("the cap must drop the last keys in sorted order")
	}
}

func TestNewAWSTagsRequiresAnInventoryClient(t *testing.T) {
	if _, err := NewAWSTags(AWSOptions{}); err == nil {
		t.Error("NewAWSTags() = nil error, want a configuration failure")
	}
}
