// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package enhancers

import (
	"context"
	"errors"
	"testing"

	"github.com/bitwise-media-group/patchy/internal/awsinv"
	"github.com/bitwise-media-group/patchy/pkg/enhance"
)

// dynamicAWSHarness wires a DynamicAWS to a mutable config and a
// build-counting fake inventory client.
type dynamicAWSHarness struct {
	cfg       *AWSTagsConfig
	cfgErr    error
	builds    []awsinv.Config
	closed    int
	inventory *fakeInventory
}

func (h *dynamicAWSHarness) enhancer() *DynamicAWS {
	return &DynamicAWS{
		Config: func(context.Context) (*AWSTagsConfig, error) { return h.cfg, h.cfgErr },
		NewInventory: func(_ context.Context, backend awsinv.Config) (AWSInventory, func() error, error) {
			h.builds = append(h.builds, backend)
			return h.inventory, func() error { h.closed++; return nil }, nil
		},
	}
}

func aggregatorBackend() awsinv.Config {
	return awsinv.Config{ConfigAggregator: &awsinv.ConfigAggregator{Name: "org", Region: "eu-west-2"}}
}

func TestDynamicAWSEnhances(t *testing.T) {
	h := &dynamicAWSHarness{
		cfg: &AWSTagsConfig{Backend: aggregatorBackend()},
		inventory: &fakeInventory{tags: map[string]string{
			"scm-repository-org": "acme", "scm-repository-name": "infra",
		}},
	}
	got, err := h.enhancer().Enhance(t.Context(), enhance.Issue{CloudResource: s3bucket()})
	if err != nil {
		t.Fatalf("Enhance() = %v, want nil", err)
	}
	if got == nil || got.Repository == nil || got.Repository.URL != "https://github.com/acme/infra" {
		t.Errorf("Enhance() = %+v, want the repository resolved", got)
	}
}

// The capability being off is "not ours", never an error: other enhancers
// still run and the finding advances.
func TestDynamicAWSStandsAsideWhenOff(t *testing.T) {
	h := &dynamicAWSHarness{}
	got, err := h.enhancer().Enhance(t.Context(), enhance.Issue{CloudResource: s3bucket()})
	if got != nil || err != nil {
		t.Errorf("Enhance() = %+v, %v; want nil, nil", got, err)
	}
	if len(h.builds) != 0 {
		t.Error("an inventory client was built with the capability off")
	}
}

// A config read failure is retryable: it feeds the hold-for-repository path
// rather than silently advancing the finding without its enrichment.
func TestDynamicAWSConfigErrorSurfaces(t *testing.T) {
	h := &dynamicAWSHarness{cfgErr: errors.New("ambiguous")}
	if _, err := h.enhancer().Enhance(t.Context(), enhance.Issue{CloudResource: s3bucket()}); err == nil {
		t.Error("Enhance() = nil error, want the config failure surfaced")
	}
}

// One client per backend: reused across enhancements, swapped (and the old
// one closed) when the operator moves to another aggregator or view.
func TestDynamicAWSMemoizesClientByBackend(t *testing.T) {
	h := &dynamicAWSHarness{
		cfg:       &AWSTagsConfig{Backend: aggregatorBackend()},
		inventory: &fakeInventory{},
	}
	d := h.enhancer()
	issue := enhance.Issue{CloudResource: s3bucket()}
	for range 2 {
		if _, err := d.Enhance(t.Context(), issue); err != nil {
			t.Fatalf("Enhance() = %v", err)
		}
	}
	if len(h.builds) != 1 {
		t.Fatalf("built %d clients for one backend, want 1", len(h.builds))
	}
	h.cfg = &AWSTagsConfig{Backend: awsinv.Config{ResourceExplorer: &awsinv.ResourceExplorer{
		ViewARN: "arn:aws:resource-explorer-2:eu-west-2:123456789012:view/org/abc",
	}}}
	if _, err := d.Enhance(t.Context(), issue); err != nil {
		t.Fatalf("Enhance() = %v", err)
	}
	if len(h.builds) != 2 || h.builds[1].ResourceExplorer == nil {
		t.Errorf("builds = %v, want a second client for the new backend", h.builds)
	}
	if h.closed != 1 {
		t.Errorf("closed %d clients on backend change, want 1", h.closed)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if h.closed != 2 {
		t.Errorf("closed %d clients after shutdown, want 2", h.closed)
	}
}

// The memo key compares backend content, not pointer identity: a fresh read
// of the same Integration yields new pointers and must not rebuild.
func TestDynamicAWSMemoKeyIsContent(t *testing.T) {
	h := &dynamicAWSHarness{
		cfg:       &AWSTagsConfig{Backend: aggregatorBackend()},
		inventory: &fakeInventory{},
	}
	d := h.enhancer()
	issue := enhance.Issue{CloudResource: s3bucket()}
	if _, err := d.Enhance(t.Context(), issue); err != nil {
		t.Fatalf("Enhance() = %v", err)
	}
	h.cfg = &AWSTagsConfig{Backend: aggregatorBackend()} // same content, new pointers
	if _, err := d.Enhance(t.Context(), issue); err != nil {
		t.Fatalf("Enhance() = %v", err)
	}
	if len(h.builds) != 1 {
		t.Errorf("built %d clients for one unchanged backend, want 1", len(h.builds))
	}
}
