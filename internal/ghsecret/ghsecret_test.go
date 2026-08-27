// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package ghsecret

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// testPrivateKeyPEM builds a throwaway RSA private key PEM for App auth.
func testPrivateKeyPEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
}

// appSecret builds a valid App credential Secret with the given extra data.
func appSecret(t *testing.T, rv string, extra map[string][]byte) *corev1.Secret {
	t.Helper()
	data := map[string][]byte{
		KeyAppID:      []byte("7"),
		KeyPrivateKey: testPrivateKeyPEM(t),
	}
	for k, v := range extra {
		data[k] = v
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "cred", ResourceVersion: rv},
		Data:       data,
	}
}

func TestProxyURL(t *testing.T) {
	tests := []struct {
		name    string
		rawURL  string
		data    map[string][]byte
		want    string
		wantErr bool
	}{
		{
			name:   "empty url stays empty",
			rawURL: "",
			data:   map[string][]byte{KeyProxyUsername: []byte("u"), KeyProxyPassword: []byte("p")},
			want:   "",
		},
		{
			name:   "no credentials passes through",
			rawURL: "http://proxy.corp.example:3128",
			want:   "http://proxy.corp.example:3128",
		},
		{
			name:   "username and password composed",
			rawURL: "http://proxy.corp.example:3128",
			data:   map[string][]byte{KeyProxyUsername: []byte("u"), KeyProxyPassword: []byte("p")},
			want:   "http://u:p@proxy.corp.example:3128",
		},
		{
			name:   "whitespace trimmed",
			rawURL: "http://proxy.corp.example:3128",
			data:   map[string][]byte{KeyProxyUsername: []byte("u\n"), KeyProxyPassword: []byte("p\n")},
			want:   "http://u:p@proxy.corp.example:3128",
		},
		{
			name:   "password without username ignored",
			rawURL: "http://proxy.corp.example:3128",
			data:   map[string][]byte{KeyProxyPassword: []byte("p")},
			want:   "http://proxy.corp.example:3128",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			secret := &corev1.Secret{Data: tt.data}
			got, err := ProxyURL(secret, tt.rawURL)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ProxyURL() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("ProxyURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestFromSecretCacheDiscrimination: two resources sharing one Secret with
// different baseURL or proxy settings must each get (and keep) their own App —
// the eviction sweep may only drop stale resourceVersions, never siblings at
// the current one.
func TestFromSecretCacheDiscrimination(t *testing.T) {
	apps := NewApps()
	secret := appSecret(t, "1", nil)

	a1, err := apps.FromSecret(secret, "", "")
	if err != nil {
		t.Fatalf("FromSecret(no proxy) error = %v", err)
	}
	a2, err := apps.FromSecret(secret, "", "http://proxy.corp.example:3128")
	if err != nil {
		t.Fatalf("FromSecret(proxy) error = %v", err)
	}
	if a1 == a2 {
		t.Error("FromSecret() returned the same App for different proxies, want distinct")
	}
	a3, err := apps.FromSecret(secret, "https://ghes.example", "")
	if err != nil {
		t.Fatalf("FromSecret(ghes) error = %v", err)
	}
	if a3 == a1 {
		t.Error("FromSecret() returned the same App for different baseURLs, want distinct")
	}

	// All three must still be memoized — alternating callers may not evict
	// each other.
	again, err := apps.FromSecret(secret, "", "")
	if err != nil {
		t.Fatalf("FromSecret(no proxy, again) error = %v", err)
	}
	if again != a1 {
		t.Error("FromSecret() rebuilt an App that should still be cached")
	}
	if got := len(apps.apps); got != 3 {
		t.Errorf("cache holds %d entries, want 3", got)
	}

	// Rotation: a new resourceVersion evicts every entry at the old one.
	rotated := appSecret(t, "2", nil)
	if _, err := apps.FromSecret(rotated, "", ""); err != nil {
		t.Fatalf("FromSecret(rotated) error = %v", err)
	}
	if got := len(apps.apps); got != 1 {
		t.Errorf("cache holds %d entries after rotation, want 1", got)
	}
}

func TestValidateProxy(t *testing.T) {
	apps := NewApps()
	pat := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "cred", ResourceVersion: "1"},
		Data:       map[string][]byte{KeyToken: []byte("pat")},
	}
	if err := apps.Validate(pat, "", "http://proxy.corp.example:3128"); err != nil {
		t.Errorf("Validate(PAT, good proxy) = %v, want nil", err)
	}
	if err := apps.Validate(pat, "", "socks5://proxy.corp.example:1080"); err == nil {
		t.Error("Validate(PAT, socks proxy) = nil, want non-nil")
	}
	if err := apps.Validate(appSecret(t, "1", nil), "", "socks5://proxy.corp.example:1080"); err == nil {
		t.Error("Validate(App, socks proxy) = nil, want non-nil")
	}
}
