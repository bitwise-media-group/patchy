// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package enhancers

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/bitwise-media-group/patchy/internal/azureinv"
	"github.com/bitwise-media-group/patchy/pkg/enhance"
	"github.com/bitwise-media-group/patchy/pkg/source"
)

// fakeAzureInventory stands in for the Azure inventory.
type fakeAzureInventory struct {
	tags  map[string]string
	err   error
	calls int
}

func (f *fakeAzureInventory) TagsFor(_ context.Context, id string) (*azureinv.Tags, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return &azureinv.Tags{Name: id, Tags: f.tags}, nil
}

// azureVM is a Wiz finding's Azure cloud resource. Name is the ARM resource
// ID, exactly as the Wiz providerId spells it.
func azureVM() *source.CloudResource {
	return &source.CloudResource{
		Provider: "azure",
		Name: "/subscriptions/00000000-0000-0000-0000-000000000000" +
			"/resourceGroups/prod/providers/Microsoft.Compute/virtualMachines/web-01",
		Type:     "Microsoft.Compute/virtualMachines",
		Project:  "00000000-0000-0000-0000-000000000000",
		Location: "uksouth",
	}
}

func newAzureEnhancer(t *testing.T, inventory AzureInventory, keys LabelKeys) *AzureTags {
	t.Helper()
	e, err := NewAzureTags(inventory, TagsOptions{Keys: keys})
	if err != nil {
		t.Fatalf("NewAzureTags: %v", err)
	}
	return e
}

func TestAzureTagsResolvesRepository(t *testing.T) {
	inventory := &fakeAzureInventory{tags: map[string]string{
		"scm-repository-org":  "acme",
		"scm-repository-name": "infra-prod",
		"owner":               "platform",
	}}
	got, err := newAzureEnhancer(t, inventory, LabelKeys{}).
		Enhance(t.Context(), enhance.Issue{CloudResource: azureVM()})
	if err != nil {
		t.Fatalf("Enhance() = %v, want nil", err)
	}
	r := got.Repository
	if r == nil || r.Provider != "github" || r.Owner != "acme" || r.Name != "infra-prod" ||
		r.URL != "https://github.com/acme/infra-prod" {
		t.Errorf("Repository = %+v, want acme/infra-prod on github.com", r)
	}
	// The context is carried whether or not a repository resolved — and the
	// tags themselves are the enrichment.
	if !reflect.DeepEqual(got.Attributes, map[string]string{
		"azure-subscription": "00000000-0000-0000-0000-000000000000",
		"resource-type":      "Microsoft.Compute/virtualMachines",
		"location":           "uksouth",
		"tag:owner":          "platform", "tag:scm-repository-org": "acme",
		"tag:scm-repository-name": "infra-prod",
	}) {
		t.Errorf("Attributes = %+v, want the subscription, type, location and tags", got.Attributes)
	}
}

// A URL is the only form that can name a self-hosted forge, so a resource
// carrying one means it deliberately — and an Azure tag value can carry the
// scheme verbatim.
func TestAzureTagsURLSupersedesTriple(t *testing.T) {
	inventory := &fakeAzureInventory{tags: map[string]string{
		"scm-repository-org":  "acme",
		"scm-repository-name": "infra-prod",
		"scm-repository-url":  "https://ghe.acme.internal/platform/infra",
	}}
	got, err := newAzureEnhancer(t, inventory, LabelKeys{}).
		Enhance(t.Context(), enhance.Issue{CloudResource: azureVM()})
	if err != nil {
		t.Fatalf("Enhance() = %v, want nil", err)
	}
	if got.Repository.URL != "https://ghe.acme.internal/platform/infra" {
		t.Errorf("URL = %q, want the explicit one", got.Repository.URL)
	}
}

func TestAzureTagsHonoursCustomKeys(t *testing.T) {
	inventory := &fakeAzureInventory{tags: map[string]string{
		"owner-org":  "acme",
		"owner-repo": "infra-prod",
	}}
	keys := LabelKeys{Org: "owner-org", Name: "owner-repo"}
	got, err := newAzureEnhancer(t, inventory, keys).
		Enhance(t.Context(), enhance.Issue{CloudResource: azureVM()})
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
func TestAzureTagsUntaggedResourceIsNotAnError(t *testing.T) {
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
			got, err := newAzureEnhancer(t, &fakeAzureInventory{tags: tt.tags}, LabelKeys{}).
				Enhance(t.Context(), enhance.Issue{CloudResource: azureVM()})
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
func TestAzureTagsMissingResourceIsFinal(t *testing.T) {
	inventory := &fakeAzureInventory{err: azureinv.ErrNotFound}
	got, err := newAzureEnhancer(t, inventory, LabelKeys{}).
		Enhance(t.Context(), enhance.Issue{CloudResource: azureVM()})
	if err != nil {
		t.Fatalf("Enhance() = %v, want nil: a missing resource is not retryable", err)
	}
	if got.Repository != nil {
		t.Errorf("Repository = %+v, want nil", got.Repository)
	}
	// The resource's own context still travels, even unresolved.
	if got.Attributes["azure-subscription"] != "00000000-0000-0000-0000-000000000000" {
		t.Errorf("Attributes = %+v, want the finding's context regardless", got.Attributes)
	}
}

// Anything else might succeed next time, so it errors — which is what makes
// the context-controller hold the finding rather than advance it.
func TestAzureTagsTransientFailureErrors(t *testing.T) {
	inventory := &fakeAzureInventory{err: errors.New("deadline exceeded")}
	if _, err := newAzureEnhancer(t, inventory, LabelKeys{}).
		Enhance(t.Context(), enhance.Issue{CloudResource: azureVM()}); err == nil {
		t.Error("Enhance() = nil, want an error so the finding is retried")
	}
}

func TestAzureTagsSkipsFindingsItDoesNotOwn(t *testing.T) {
	for _, tt := range []struct {
		name  string
		issue enhance.Issue
	}{
		{"a code finding", enhance.Issue{Repo: source.Repo{Owner: "acme", Name: "shop"}}},
		{
			"another cloud platform",
			enhance.Issue{CloudResource: s3bucket()},
		},
		{
			// Wiz Defend falls back to a synthetic account pseudo-resource
			// when a threat names no concrete one; no inventory records it.
			"a synthetic account resource",
			enhance.Issue{CloudResource: &source.CloudResource{
				Provider: "azure", Name: "wiz-account:00000000-0000-0000-0000-000000000000",
			}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			inventory := &fakeAzureInventory{}
			got, err := newAzureEnhancer(t, inventory, LabelKeys{}).Enhance(t.Context(), tt.issue)
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
func TestAzureTagsCapsTagAttributes(t *testing.T) {
	tags := map[string]string{}
	for i := range 40 {
		tags[fmt.Sprintf("tag-%02d", i)] = "v"
	}
	got, err := newAzureEnhancer(t, &fakeAzureInventory{tags: tags}, LabelKeys{}).
		Enhance(t.Context(), enhance.Issue{CloudResource: azureVM()})
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

func TestNewAzureTagsRequiresAnInventoryClient(t *testing.T) {
	if _, err := NewAzureTags(nil, TagsOptions{}); err == nil {
		t.Error("NewAzureTags() = nil error, want a configuration failure")
	}
}
