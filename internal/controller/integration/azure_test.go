// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package integration

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
)

func TestValidateAzure(t *testing.T) {
	for _, tt := range []struct {
		name  string
		azure *v1alpha1.AzureIntegration
		ok    bool
	}{
		{"resource tags configured", &v1alpha1.AzureIntegration{
			ResourceTags: &v1alpha1.AzureResourceTags{
				Enabled:         true,
				ManagementGroup: "platform-mg",
			},
		}, true},
		{"no capability", &v1alpha1.AzureIntegration{}, false},
		{"no provider block", nil, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			integ := &v1alpha1.Integration{
				ObjectMeta: metav1.ObjectMeta{Name: "azure", Namespace: "patchy"},
				Spec: v1alpha1.IntegrationSpec{
					Provider: v1alpha1.IntegrationProviderAzure,
					Azure:    tt.azure,
				},
			}
			if err := validateAzure(integ); (err == nil) != tt.ok {
				t.Errorf("validateAzure() = %v, want ok=%v", err, tt.ok)
			}
		})
	}
}
