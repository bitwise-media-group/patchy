// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package webhook

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

func TestTokenAuthenticator(t *testing.T) {
	tokens := [][]byte{[]byte("first-token"), []byte("second-token")}

	for _, tt := range []struct {
		name    string
		header  string
		secrets [][]byte
		wantOK  bool
	}{
		{"matching token passes", "Bearer first-token", tokens, true},
		{"any candidate may match", "Bearer second-token", tokens, true},
		{"scheme is case-insensitive", "bearer first-token", tokens, true},
		{"wrong token fails", "Bearer wrong", tokens, false},
		{"missing header fails", "", tokens, false},
		{"other scheme fails", "Basic Zm9vOmJhcg==", tokens, false},
		{"scheme without credential fails", "Bearer ", tokens, false},
		{"token as prefix fails", "Bearer first", tokens, false},
		{"no candidates fails", "Bearer first-token", nil, false},
		// An empty candidate must never match anything, least of all an
		// absent header — that would authenticate everyone.
		{"empty candidate never matches", "", [][]byte{{}}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			auth := &TokenAuthenticator{SecretsFor: func(context.Context) [][]byte { return tt.secrets }}
			r, err := http.NewRequest(http.MethodPost, "/wiz/webhooks", nil)
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			if tt.header != "" {
				r.Header.Set("Authorization", tt.header)
			}
			got := auth.Authenticate(context.Background(), r, []byte("{}"))
			if ok := got == nil; ok != tt.wantOK {
				t.Errorf("Authenticate() = %v, want ok=%v", got, tt.wantOK)
			}
			if got != nil && !errors.Is(got, ErrUnauthenticated) {
				t.Errorf("Authenticate() = %v, want ErrUnauthenticated", got)
			}
		})
	}
}

// A nil SecretsFor is a fail-closed configuration, not a panic.
func TestTokenAuthenticatorNilSecrets(t *testing.T) {
	auth := &TokenAuthenticator{}
	r, err := http.NewRequest(http.MethodPost, "/wiz/webhooks", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	r.Header.Set("Authorization", "Bearer anything")
	if got := auth.Authenticate(context.Background(), r, nil); !errors.Is(got, ErrUnauthenticated) {
		t.Errorf("Authenticate() = %v, want ErrUnauthenticated", got)
	}
}
