// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package context

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/enhancers"
	"github.com/bitwise-media-group/patchy/internal/kube"
)

func caiIntegration(name, scope string) *v1alpha1.Integration {
	return &v1alpha1.Integration{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "patchy"},
		Spec: v1alpha1.IntegrationSpec{
			Provider: v1alpha1.IntegrationProviderGoogleCloud,
			GoogleCloud: &v1alpha1.GoogleCloudIntegration{
				CloudAssetInventory: &v1alpha1.GoogleCloudAssetInventory{
					Enabled:        true,
					Scope:          scope,
					RepositoryHost: "github.example.com",
					Labels:         &v1alpha1.AssetLabelKeys{Org: "owner-org"},
				},
			},
		},
	}
}

func TestAssetConfigSource(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(kube.Scheme()).
		WithObjects(caiIntegration("gcp", "organizations/123")).Build()
	cfg, err := AssetConfigSource(c, "patchy")(t.Context())
	if err != nil {
		t.Fatalf("AssetConfigSource() = %v, want nil", err)
	}
	want := &enhancers.AssetConfig{
		Scope:          "organizations/123",
		RepositoryHost: "github.example.com",
		Keys:           enhancers.LabelKeys{Org: "owner-org"},
	}
	if *cfg != *want {
		t.Errorf("config = %+v, want %+v", cfg, want)
	}
}

func TestAssetConfigSourceNoIntegration(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(kube.Scheme()).Build()
	cfg, err := AssetConfigSource(c, "patchy")(t.Context())
	if cfg != nil || err != nil {
		t.Errorf("AssetConfigSource() = %+v, %v; want nil, nil (capability off)", cfg, err)
	}
}

// Two Integrations claiming the capability is the operator's error to fix;
// the source fails closed rather than picking one.
func TestAssetConfigSourceAmbiguous(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(kube.Scheme()).
		WithObjects(
			caiIntegration("gcp-a", "organizations/123"),
			caiIntegration("gcp-b", "organizations/456"),
		).Build()
	if _, err := AssetConfigSource(c, "patchy")(t.Context()); err == nil {
		t.Error("AssetConfigSource() = nil error, want the ambiguity surfaced")
	}
}
