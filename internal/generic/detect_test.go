// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package generic

import (
	"net/http"
	"testing"
)

func TestDetect(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    string
		wantErr bool
	}{
		{"findings", `{"version":"v1","event":"findings"}`, EventFindings, false},
		{"ping", `{"version":"v1","event":"ping"}`, "ping", false},
		{"unknown event", `{"version":"v1","event":"bogus"}`, "", true},
		{"missing version", `{"event":"findings"}`, "", true},
		{"future version", `{"version":"v2","event":"findings"}`, "", true},
		{"undecodable", `not json`, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Detect([]byte(tt.body))
			if (err != nil) != tt.wantErr {
				t.Fatalf("Detect() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("Detect() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDeliveryID(t *testing.T) {
	body := []byte(`{"version":"v1","event":"findings"}`)
	withHeader, _ := http.NewRequest(http.MethodPost, "/generic/x/webhooks", nil)
	withHeader.Header.Set("X-Patchy-Delivery", "d-42")
	if got := DeliveryID(withHeader, body); got != "d-42" {
		t.Errorf("DeliveryID(with header) = %q, want the header value", got)
	}
	without, _ := http.NewRequest(http.MethodPost, "/generic/x/webhooks", nil)
	first := DeliveryID(without, body)
	if len(first) != 16 {
		t.Errorf("DeliveryID(no header) = %q, want a 16-char body hash", first)
	}
	if again := DeliveryID(without, body); again != first {
		t.Errorf("DeliveryID is not deterministic: %q then %q", first, again)
	}
	if other := DeliveryID(without, []byte(`{"different":true}`)); other == first {
		t.Error("DeliveryID collides across distinct bodies")
	}
}

func TestPathForRoundTrip(t *testing.T) {
	for _, name := range []string{"warehouse", "a-b-c", "dev"} {
		t.Run(name, func(t *testing.T) {
			got, ok := NameFromPath(PathFor(name))
			if !ok || got != name {
				t.Errorf("NameFromPath(PathFor(%q)) = (%q, %v), want (%q, true)", name, got, ok, name)
			}
		})
	}
}

func TestNameFromPath(t *testing.T) {
	tests := []struct {
		path string
		want string
		ok   bool
	}{
		{"/generic/warehouse/webhooks", "warehouse", true},
		{"/generic/a-b-c/webhooks", "a-b-c", true},
		{"/generic//webhooks", "", false},
		{"/generic/a/b/webhooks", "", false},
		{"/github/webhooks", "", false},
		{"/generic/warehouse", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got, ok := NameFromPath(tt.path)
			if got != tt.want || ok != tt.ok {
				t.Errorf("NameFromPath(%q) = (%q, %v), want (%q, %v)", tt.path, got, ok, tt.want, tt.ok)
			}
		})
	}
}
