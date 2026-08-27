// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package forge

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/kube"
)

// proxyForge builds a PAT-credentialed Forge with the given spec proxy.
func proxyForge(proxyURL string) *v1alpha1.Forge {
	f := &v1alpha1.Forge{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "gh"},
		Spec: v1alpha1.ForgeSpec{
			Provider:  v1alpha1.ForgeProviderGitHub,
			SecretRef: v1alpha1.LocalSecretReference{Name: "cred"},
		},
	}
	if proxyURL != "" {
		f.Spec.Proxy = &v1alpha1.ProxyConfig{URL: proxyURL}
	}
	return f
}

// storeWith builds a Store over a fake client holding the credential Secret.
func storeWith(t *testing.T, data map[string][]byte) *Store {
	t.Helper()
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "cred"},
		Data:       data,
	}
	c := fake.NewClientBuilder().WithScheme(kube.Scheme()).WithObjects(secret).Build()
	return NewStore(c)
}

// TestValidateBadProxy: a PAT Forge with an unusable proxy URL fails
// validation with no network involved.
func TestValidateBadProxy(t *testing.T) {
	s := storeWith(t, map[string][]byte{"token": []byte("pat")})
	if err := s.Validate(context.Background(), proxyForge("socks5://proxy.corp.example:1080")); err == nil {
		t.Error("Validate(socks proxy) = nil, want non-nil")
	}
	if err := s.Validate(context.Background(), proxyForge("http://proxy.corp.example:3128")); err != nil {
		t.Errorf("Validate(good proxy) = %v, want nil", err)
	}
}

func TestStoreProxyURL(t *testing.T) {
	tests := []struct {
		name  string
		proxy string
		data  map[string][]byte
		want  string
	}{
		{
			name:  "no spec proxy",
			proxy: "",
			data:  map[string][]byte{"token": []byte("pat")},
			want:  "",
		},
		{
			name:  "spec proxy without credentials",
			proxy: "http://proxy.corp.example:3128",
			data:  map[string][]byte{"token": []byte("pat")},
			want:  "http://proxy.corp.example:3128",
		},
		{
			name:  "spec proxy with secret basic auth",
			proxy: "http://proxy.corp.example:3128",
			data: map[string][]byte{
				"token": []byte("pat"), "proxyUsername": []byte("u"), "proxyPassword": []byte("p"),
			},
			want: "http://u:p@proxy.corp.example:3128",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := storeWith(t, tt.data)
			res := &Resolved{Forge: proxyForge(tt.proxy)}
			got, err := s.ProxyURL(context.Background(), res)
			if err != nil {
				t.Fatalf("ProxyURL() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("ProxyURL() = %q, want %q", got, tt.want)
			}
		})
	}
}
