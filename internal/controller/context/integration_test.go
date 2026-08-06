// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package context

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
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

func awsIntegration(name string, rt *v1alpha1.AWSResourceTags) *v1alpha1.Integration {
	return &v1alpha1.Integration{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "patchy"},
		Spec: v1alpha1.IntegrationSpec{
			Provider: v1alpha1.IntegrationProviderAWS,
			AWS:      &v1alpha1.AWSIntegration{ResourceTags: rt},
		},
	}
}

func TestAWSTagsConfigSource(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(kube.Scheme()).
		WithObjects(awsIntegration("aws", &v1alpha1.AWSResourceTags{
			Enabled:          true,
			ConfigAggregator: &v1alpha1.AWSConfigAggregator{Name: "org", Region: "eu-west-2"},
			RepositoryHost:   "github.example.com",
			Tags:             &v1alpha1.AssetLabelKeys{Org: "owner-org"},
		})).Build()
	cfg, err := AWSTagsConfigSource(c, "patchy")(t.Context())
	if err != nil {
		t.Fatalf("AWSTagsConfigSource() = %v, want nil", err)
	}
	if cfg.RepositoryHost != "github.example.com" || cfg.Keys.Org != "owner-org" {
		t.Errorf("config = %+v, want the host and keys carried over", cfg)
	}
	a := cfg.Backend.ConfigAggregator
	if a == nil || a.Name != "org" || a.Region != "eu-west-2" || cfg.Backend.ResourceExplorer != nil {
		t.Errorf("backend = %+v, want the aggregator alone", cfg.Backend)
	}
}

func TestAWSTagsConfigSourceResourceExplorer(t *testing.T) {
	view := "arn:aws:resource-explorer-2:eu-west-2:123456789012:view/org/abc"
	c := fake.NewClientBuilder().WithScheme(kube.Scheme()).
		WithObjects(awsIntegration("aws", &v1alpha1.AWSResourceTags{
			Enabled:          true,
			ResourceExplorer: &v1alpha1.AWSResourceExplorer{ViewARN: view},
		})).Build()
	cfg, err := AWSTagsConfigSource(c, "patchy")(t.Context())
	if err != nil {
		t.Fatalf("AWSTagsConfigSource() = %v, want nil", err)
	}
	e := cfg.Backend.ResourceExplorer
	if e == nil || e.ViewARN != view || cfg.Backend.ConfigAggregator != nil {
		t.Errorf("backend = %+v, want the view alone", cfg.Backend)
	}
}

func TestAWSTagsConfigSourceNoIntegration(t *testing.T) {
	// A disabled capability and no aws Integration at all read the same: off.
	c := fake.NewClientBuilder().WithScheme(kube.Scheme()).
		WithObjects(awsIntegration("aws", &v1alpha1.AWSResourceTags{
			ConfigAggregator: &v1alpha1.AWSConfigAggregator{Name: "org", Region: "eu-west-2"},
		})).Build()
	cfg, err := AWSTagsConfigSource(c, "patchy")(t.Context())
	if cfg != nil || err != nil {
		t.Errorf("AWSTagsConfigSource() = %+v, %v; want nil, nil (capability off)", cfg, err)
	}
}

func azureIntegration(name string, rt *v1alpha1.AzureResourceTags) *v1alpha1.Integration {
	return &v1alpha1.Integration{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "patchy"},
		Spec: v1alpha1.IntegrationSpec{
			Provider: v1alpha1.IntegrationProviderAzure,
			Azure:    &v1alpha1.AzureIntegration{ResourceTags: rt},
		},
	}
}

func TestAzureTagsConfigSource(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(kube.Scheme()).
		WithObjects(azureIntegration("azure", &v1alpha1.AzureResourceTags{
			Enabled:         true,
			ManagementGroup: "platform-mg",
			RepositoryHost:  "github.example.com",
			Tags:            &v1alpha1.AssetLabelKeys{Org: "owner-org"},
		})).Build()
	cfg, err := AzureTagsConfigSource(c, "patchy")(t.Context())
	if err != nil {
		t.Fatalf("AzureTagsConfigSource() = %v, want nil", err)
	}
	want := &enhancers.AzureTagsConfig{
		ManagementGroup: "platform-mg",
		RepositoryHost:  "github.example.com",
		Keys:            enhancers.LabelKeys{Org: "owner-org"},
	}
	if *cfg != *want {
		t.Errorf("config = %+v, want %+v", cfg, want)
	}
}

func TestAzureTagsConfigSourceNoIntegration(t *testing.T) {
	// A disabled capability and no azure Integration at all read the same: off.
	c := fake.NewClientBuilder().WithScheme(kube.Scheme()).
		WithObjects(azureIntegration("azure", &v1alpha1.AzureResourceTags{
			ManagementGroup: "platform-mg",
		})).Build()
	cfg, err := AzureTagsConfigSource(c, "patchy")(t.Context())
	if cfg != nil || err != nil {
		t.Errorf("AzureTagsConfigSource() = %+v, %v; want nil, nil (capability off)", cfg, err)
	}
}

func genericIntegration(name, url string) *v1alpha1.Integration {
	return &v1alpha1.Integration{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "patchy"},
		Spec: v1alpha1.IntegrationSpec{
			Provider:  v1alpha1.IntegrationProviderGeneric,
			SecretRef: &v1alpha1.LocalSecretReference{Name: name + "-creds"},
			Generic: &v1alpha1.GenericIntegration{
				Enhance: &v1alpha1.GenericEnhancer{
					Enabled: true,
					URL:     url,
					Timeout: metav1.Duration{Duration: 45 * time.Second},
				},
			},
		},
	}
}

func TestGenericEnhancerConfigSource(t *testing.T) {
	suspended := genericIntegration("suspended", "https://s.internal/enhance")
	suspended.Spec.Suspend = true
	enhanceOff := genericIntegration("off", "https://off.internal/enhance")
	enhanceOff.Spec.Generic.Enhance.Enabled = false
	c := fake.NewClientBuilder().WithScheme(kube.Scheme()).
		WithObjects(
			genericIntegration("warehouse", "https://wh.internal/enhance"),
			genericIntegration("cmdb", "https://cmdb.internal/enhance"),
			suspended,
			enhanceOff,
			caiIntegration("gcp", "organizations/123"),
			&corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "warehouse-creds", Namespace: "patchy"},
				Data:       map[string][]byte{"webhookSecret": []byte("wh-secret")},
			},
			// cmdb's Secret is deliberately absent: the config must still
			// list it, secret-less, so the failure stays attributed.
		).Build()

	cfgs, err := GenericEnhancerConfigSource(c, c, "patchy", nil)(t.Context())
	if err != nil {
		t.Fatalf("GenericEnhancerConfigSource() = %v, want nil", err)
	}
	if len(cfgs) != 2 {
		t.Fatalf("configs = %+v, want warehouse and cmdb only", cfgs)
	}
	byName := map[string]enhancers.GenericConfig{}
	for _, cfg := range cfgs {
		byName[cfg.Name] = cfg
	}
	wh := byName["warehouse"]
	if wh.URL != "https://wh.internal/enhance" || string(wh.Secret) != "wh-secret" ||
		wh.Timeout != 45*time.Second {
		t.Errorf("warehouse config = %+v, want url, secret, and timeout from the CR", wh)
	}
	if cmdb := byName["cmdb"]; cmdb.URL == "" || cmdb.Secret != nil {
		t.Errorf("cmdb config = %+v, want listed secret-less", cmdb)
	}
}
