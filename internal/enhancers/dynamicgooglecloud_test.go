// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package enhancers

import (
	"context"
	"errors"
	"testing"

	"github.com/bitwise-media-group/patchy/pkg/enhance"
)

// dynamicHarness wires a DynamicGoogleCloud to a mutable config and a
// build-counting fake asset client.
type dynamicHarness struct {
	cfg    *AssetConfig
	cfgErr error
	builds []string
	closed int
	assets *fakeAssets
}

func (h *dynamicHarness) enhancer() *DynamicGoogleCloud {
	return &DynamicGoogleCloud{
		Config: func(context.Context) (*AssetConfig, error) { return h.cfg, h.cfgErr },
		NewAssets: func(_ context.Context, scope string) (AssetLabels, func() error, error) {
			h.builds = append(h.builds, scope)
			return h.assets, func() error { h.closed++; return nil }, nil
		},
	}
}

func TestDynamicGoogleCloudEnhances(t *testing.T) {
	h := &dynamicHarness{
		cfg:    &AssetConfig{Scope: "organizations/123"},
		assets: &fakeAssets{labels: map[string]string{"scm-repository-org": "acme", "scm-repository-name": "infra"}},
	}
	d := h.enhancer()
	got, err := d.Enhance(t.Context(), enhance.Issue{CloudResource: bucket()})
	if err != nil {
		t.Fatalf("Enhance() = %v, want nil", err)
	}
	if got == nil || got.Repository == nil || got.Repository.URL != "https://github.com/acme/infra" {
		t.Errorf("Enhance() = %+v, want the repository resolved", got)
	}
}

// The capability being off is "not ours", never an error: other enhancers
// still run and the finding advances.
func TestDynamicGoogleCloudStandsAsideWhenOff(t *testing.T) {
	h := &dynamicHarness{}
	got, err := h.enhancer().Enhance(t.Context(), enhance.Issue{CloudResource: bucket()})
	if got != nil || err != nil {
		t.Errorf("Enhance() = %+v, %v; want nil, nil", got, err)
	}
	if len(h.builds) != 0 {
		t.Error("an asset client was built with the capability off")
	}
}

// A config read failure is retryable: it feeds the hold-for-repository path
// rather than silently advancing the finding without its enrichment.
func TestDynamicGoogleCloudConfigErrorSurfaces(t *testing.T) {
	h := &dynamicHarness{cfgErr: errors.New("ambiguous")}
	if _, err := h.enhancer().Enhance(t.Context(), enhance.Issue{CloudResource: bucket()}); err == nil {
		t.Error("Enhance() = nil error, want the config failure surfaced")
	}
}

// One client per scope: reused across enhancements, swapped (and the old one
// closed) when the operator moves the scope.
func TestDynamicGoogleCloudMemoizesClientByScope(t *testing.T) {
	h := &dynamicHarness{
		cfg:    &AssetConfig{Scope: "organizations/123"},
		assets: &fakeAssets{},
	}
	d := h.enhancer()
	issue := enhance.Issue{CloudResource: bucket()}
	for range 2 {
		if _, err := d.Enhance(t.Context(), issue); err != nil {
			t.Fatalf("Enhance() = %v", err)
		}
	}
	if len(h.builds) != 1 {
		t.Fatalf("built %d clients for one scope, want 1", len(h.builds))
	}
	h.cfg = &AssetConfig{Scope: "projects/acme-prod"}
	if _, err := d.Enhance(t.Context(), issue); err != nil {
		t.Fatalf("Enhance() = %v", err)
	}
	if len(h.builds) != 2 || h.builds[1] != "projects/acme-prod" {
		t.Errorf("builds = %v, want a second client for the new scope", h.builds)
	}
	if h.closed != 1 {
		t.Errorf("closed %d clients on scope change, want 1", h.closed)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if h.closed != 2 {
		t.Errorf("closed %d clients after shutdown, want 2", h.closed)
	}
}
