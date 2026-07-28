// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package enhancers

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/bitwise-media-group/patchy/internal/gcpasset"
	"github.com/bitwise-media-group/patchy/pkg/enhance"
	"github.com/bitwise-media-group/patchy/pkg/source"
)

// fakeAssets stands in for Cloud Asset Inventory.
type fakeAssets struct {
	labels map[string]string
	err    error
	calls  int
}

func (f *fakeAssets) LabelsFor(_ context.Context, name, _ string) (*gcpasset.Labels, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return &gcpasset.Labels{Name: name, Labels: f.labels}, nil
}

// bucket is an SCC finding's cloud resource.
func bucket() *source.CloudResource {
	return &source.CloudResource{
		Provider: "google",
		Name:     "//storage.googleapis.com/projects/acme-prod/buckets/artifacts",
		Type:     "google.cloud.storage.Bucket",
		Project:  "projects/acme-prod",
		Location: "europe-west2",
	}
}

func newEnhancer(t *testing.T, assets AssetLabels, keys LabelKeys) *GoogleCloudLabels {
	t.Helper()
	e, err := NewGoogleCloudLabels(GoogleCloudOptions{Assets: assets, Keys: keys})
	if err != nil {
		t.Fatalf("NewGoogleCloudLabels: %v", err)
	}
	return e
}

func TestGoogleCloudLabelsResolvesRepository(t *testing.T) {
	assets := &fakeAssets{labels: map[string]string{
		"scm-repository-org":  "acme",
		"scm-repository-name": "infra-prod",
		"unrelated":           "ignored",
	}}
	got, err := newEnhancer(t, assets, LabelKeys{}).
		Enhance(t.Context(), enhance.Issue{CloudResource: bucket()})
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
	// The context is carried whether or not a repository resolved.
	wantAttrs := map[string]string{
		"gcp-project": "acme-prod", "resource-type": "google.cloud.storage.Bucket",
		"location": "europe-west2",
	}
	if !reflect.DeepEqual(got.Attributes, wantAttrs) {
		t.Errorf("Attributes = %+v, want %+v", got.Attributes, wantAttrs)
	}
}

// A URL is the only form that can name a self-hosted forge, so a resource
// carrying one means it deliberately.
func TestGoogleCloudLabelsURLSupersedesTriple(t *testing.T) {
	assets := &fakeAssets{labels: map[string]string{
		"scm-repository-org":  "acme",
		"scm-repository-name": "infra-prod",
		"scm-repository-url":  "https://ghe.acme.internal/platform/infra",
	}}
	got, err := newEnhancer(t, assets, LabelKeys{}).
		Enhance(t.Context(), enhance.Issue{CloudResource: bucket()})
	if err != nil {
		t.Fatalf("Enhance() = %v, want nil", err)
	}
	if got.Repository.URL != "https://ghe.acme.internal/platform/infra" {
		t.Errorf("URL = %q, want the explicit one", got.Repository.URL)
	}
}

// A Google Cloud label value cannot contain "://", so an operator forced to
// use one will write the URL without a scheme.
func TestGoogleCloudLabelsNormalizesSchemelessURL(t *testing.T) {
	for _, in := range []string{"github.com/acme/infra", "//github.com/acme/infra"} {
		assets := &fakeAssets{labels: map[string]string{"scm-repository-url": in}}
		got, err := newEnhancer(t, assets, LabelKeys{}).
			Enhance(t.Context(), enhance.Issue{CloudResource: bucket()})
		if err != nil {
			t.Fatalf("Enhance(%q) = %v, want nil", in, err)
		}
		if got.Repository.URL != "https://github.com/acme/infra" {
			t.Errorf("URL for %q = %q, want the https form", in, got.Repository.URL)
		}
	}
}

func TestGoogleCloudLabelsHonoursCustomKeys(t *testing.T) {
	assets := &fakeAssets{labels: map[string]string{
		"owner-org":  "acme",
		"owner-repo": "infra-prod",
	}}
	keys := LabelKeys{Org: "owner-org", Name: "owner-repo"}
	got, err := newEnhancer(t, assets, keys).
		Enhance(t.Context(), enhance.Issue{CloudResource: bucket()})
	if err != nil {
		t.Fatalf("Enhance() = %v, want nil", err)
	}
	if got.Repository == nil || got.Repository.Name != "infra-prod" {
		t.Errorf("Repository = %+v, want the custom keys honoured", got.Repository)
	}
}

// A resource that exists but carries no ownership labels is a final answer,
// not a failure: reporting it as an error would hold the finding out of sight
// instead of handing it to a human.
func TestGoogleCloudLabelsUnlabelledResourceIsNotAnError(t *testing.T) {
	for _, tt := range []struct {
		name   string
		labels map[string]string
	}{
		{"no labels at all", nil},
		{"labels but none of ours", map[string]string{"env": "prod"}},
		{"an org with no name", map[string]string{"scm-repository-org": "acme"}},
		{"a name with no org", map[string]string{"scm-repository-name": "infra"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := newEnhancer(t, &fakeAssets{labels: tt.labels}, LabelKeys{}).
				Enhance(t.Context(), enhance.Issue{CloudResource: bucket()})
			if err != nil {
				t.Fatalf("Enhance() = %v, want nil", err)
			}
			if got == nil || got.Repository != nil {
				t.Errorf("Repository = %+v, want nil with no error", got)
			}
		})
	}
}

// A deleted or out-of-scope resource will not appear next time either, so it
// is reported as a clean no-answer rather than something to retry.
func TestGoogleCloudLabelsMissingResourceIsFinal(t *testing.T) {
	assets := &fakeAssets{err: gcpasset.ErrNotFound}
	got, err := newEnhancer(t, assets, LabelKeys{}).
		Enhance(t.Context(), enhance.Issue{CloudResource: bucket()})
	if err != nil {
		t.Fatalf("Enhance() = %v, want nil: a missing resource is not retryable", err)
	}
	if got.Repository != nil {
		t.Errorf("Repository = %+v, want nil", got.Repository)
	}
	// The resource's own context still travels, even unresolved.
	if got.Attributes["gcp-project"] != "acme-prod" {
		t.Errorf("Attributes = %+v, want the finding's context regardless", got.Attributes)
	}
}

// Anything else might succeed next time, so it errors — which is what makes
// the context-controller hold the finding rather than advance it.
func TestGoogleCloudLabelsTransientFailureErrors(t *testing.T) {
	assets := &fakeAssets{err: errors.New("deadline exceeded")}
	if _, err := newEnhancer(t, assets, LabelKeys{}).
		Enhance(t.Context(), enhance.Issue{CloudResource: bucket()}); err == nil {
		t.Error("Enhance() = nil, want an error so the finding is retried")
	}
}

func TestGoogleCloudLabelsSkipsFindingsItDoesNotOwn(t *testing.T) {
	for _, tt := range []struct {
		name  string
		issue enhance.Issue
	}{
		{"a code finding", enhance.Issue{Repo: source.Repo{Owner: "acme", Name: "shop"}}},
		{
			"another cloud platform",
			enhance.Issue{CloudResource: &source.CloudResource{Provider: "aws", Name: "arn:..."}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assets := &fakeAssets{}
			got, err := newEnhancer(t, assets, LabelKeys{}).Enhance(t.Context(), tt.issue)
			if err != nil || got != nil {
				t.Errorf("Enhance() = (%+v, %v), want (nil, nil)", got, err)
			}
			if assets.calls != 0 {
				t.Errorf("looked up %d resources, want none", assets.calls)
			}
		})
	}
}

func TestNewGoogleCloudLabelsRequiresAnAssetClient(t *testing.T) {
	if _, err := NewGoogleCloudLabels(GoogleCloudOptions{}); err == nil {
		t.Error("NewGoogleCloudLabels() = nil error, want a configuration failure")
	}
}

func TestValidateScope(t *testing.T) {
	for _, tt := range []struct {
		scope string
		ok    bool
	}{
		{"organizations/123456", true},
		{"folders/123456", true},
		{"projects/acme-prod", true},
		{"organizations/", false},
		{"acme-prod", false},
		{"", false},
	} {
		if err := gcpasset.ValidateScope(tt.scope); (err == nil) != tt.ok {
			t.Errorf("ValidateScope(%q) = %v, want ok=%v", tt.scope, err, tt.ok)
		}
	}
}
