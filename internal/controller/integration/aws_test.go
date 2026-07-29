// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package integration

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
)

func TestValidateAWS(t *testing.T) {
	for _, tt := range []struct {
		name string
		aws  *v1alpha1.AWSIntegration
		ok   bool
	}{
		{"resource tags configured", &v1alpha1.AWSIntegration{
			ResourceTags: &v1alpha1.AWSResourceTags{
				Enabled:          true,
				ConfigAggregator: &v1alpha1.AWSConfigAggregator{Name: "org", Region: "eu-west-2"},
			},
		}, true},
		{"no capability", &v1alpha1.AWSIntegration{}, false},
		{"no provider block", nil, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			integ := &v1alpha1.Integration{
				ObjectMeta: metav1.ObjectMeta{Name: "aws", Namespace: "patchy"},
				Spec: v1alpha1.IntegrationSpec{
					Provider: v1alpha1.IntegrationProviderAWS,
					AWS:      tt.aws,
				},
			}
			if err := validateAWS(integ); (err == nil) != tt.ok {
				t.Errorf("validateAWS() = %v, want ok=%v", err, tt.ok)
			}
		})
	}
}
