// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package integration

import (
	"testing"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
)

func TestGithubProxyURL(t *testing.T) {
	tests := []struct {
		name  string
		integ *v1alpha1.Integration
		want  string
	}{
		{
			name:  "no github block",
			integ: &v1alpha1.Integration{},
			want:  "",
		},
		{
			name: "no proxy block",
			integ: &v1alpha1.Integration{Spec: v1alpha1.IntegrationSpec{
				GitHub: &v1alpha1.GitHubIntegration{},
			}},
			want: "",
		},
		{
			name: "proxy set",
			integ: &v1alpha1.Integration{Spec: v1alpha1.IntegrationSpec{
				GitHub: &v1alpha1.GitHubIntegration{
					Proxy: &v1alpha1.ProxyConfig{URL: "http://proxy.corp.example:3128"},
				},
			}},
			want: "http://proxy.corp.example:3128",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := githubProxyURL(tt.integ); got != tt.want {
				t.Errorf("githubProxyURL() = %q, want %q", got, tt.want)
			}
		})
	}
}
