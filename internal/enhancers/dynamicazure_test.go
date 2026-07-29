// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package enhancers

import (
	"context"
	"errors"
	"testing"

	"github.com/bitwise-media-group/patchy/internal/azureinv"
	"github.com/bitwise-media-group/patchy/pkg/enhance"
)

// dynamicAzureHarness wires a DynamicAzure to a mutable config and a
// build-counting fake inventory client.
type dynamicAzureHarness struct {
	cfg       *AzureTagsConfig
	cfgErr    error
	builds    []azureinv.Config
	closed    int
	inventory *fakeAzureInventory
}

func (h *dynamicAzureHarness) enhancer() *DynamicAzure {
	return &DynamicAzure{
		Config: func(context.Context) (*AzureTagsConfig, error) { return h.cfg, h.cfgErr },
		NewInventory: func(_ context.Context, cfg azureinv.Config) (AzureInventory, func() error, error) {
			h.builds = append(h.builds, cfg)
			return h.inventory, func() error { h.closed++; return nil }, nil
		},
	}
}

func TestDynamicAzureEnhances(t *testing.T) {
	h := &dynamicAzureHarness{
		cfg: &AzureTagsConfig{},
		inventory: &fakeAzureInventory{tags: map[string]string{
			"scm-repository-org": "acme", "scm-repository-name": "infra",
		}},
	}
	got, err := h.enhancer().Enhance(t.Context(), enhance.Issue{CloudResource: azureVM()})
	if err != nil {
		t.Fatalf("Enhance() = %v, want nil", err)
	}
	if got == nil || got.Repository == nil || got.Repository.URL != "https://github.com/acme/infra" {
		t.Errorf("Enhance() = %+v, want the repository resolved", got)
	}
}

// The capability being off is "not ours", never an error: other enhancers
// still run and the finding advances.
func TestDynamicAzureStandsAsideWhenOff(t *testing.T) {
	h := &dynamicAzureHarness{}
	got, err := h.enhancer().Enhance(t.Context(), enhance.Issue{CloudResource: azureVM()})
	if got != nil || err != nil {
		t.Errorf("Enhance() = %+v, %v; want nil, nil", got, err)
	}
	if len(h.builds) != 0 {
		t.Error("an inventory client was built with the capability off")
	}
}

// A config read failure is retryable: it feeds the hold-for-repository path
// rather than silently advancing the finding without its enrichment.
func TestDynamicAzureConfigErrorSurfaces(t *testing.T) {
	h := &dynamicAzureHarness{cfgErr: errors.New("ambiguous")}
	if _, err := h.enhancer().Enhance(t.Context(), enhance.Issue{CloudResource: azureVM()}); err == nil {
		t.Error("Enhance() = nil error, want the config failure surfaced")
	}
}

// One client per scope: reused across enhancements — including across fresh
// reads of the same Integration, since the memo key is the management group
// string itself — and swapped (the old one closed) when the operator moves to
// another management group.
func TestDynamicAzureMemoizesClientByManagementGroup(t *testing.T) {
	h := &dynamicAzureHarness{
		cfg:       &AzureTagsConfig{ManagementGroup: "platform-mg"},
		inventory: &fakeAzureInventory{},
	}
	d := h.enhancer()
	issue := enhance.Issue{CloudResource: azureVM()}
	for range 2 {
		h.cfg = &AzureTagsConfig{ManagementGroup: "platform-mg"} // fresh read, same scope
		if _, err := d.Enhance(t.Context(), issue); err != nil {
			t.Fatalf("Enhance() = %v", err)
		}
	}
	if len(h.builds) != 1 {
		t.Fatalf("built %d clients for one scope, want 1", len(h.builds))
	}
	h.cfg = &AzureTagsConfig{ManagementGroup: "sandbox-mg"}
	if _, err := d.Enhance(t.Context(), issue); err != nil {
		t.Fatalf("Enhance() = %v", err)
	}
	if len(h.builds) != 2 || h.builds[1].ManagementGroup != "sandbox-mg" {
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
