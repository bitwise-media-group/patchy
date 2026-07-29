// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package azureinv

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resourcegraph/armresourcegraph"
)

const vmID = "/subscriptions/00000000-0000-0000-0000-000000000000" +
	"/resourceGroups/prod/providers/Microsoft.Compute/virtualMachines/web-01"

type fakeGraph struct {
	query            string
	managementGroups []string
	rows             []any
	err              error
}

func (f *fakeGraph) Resources(_ context.Context, query armresourcegraph.QueryRequest,
	_ *armresourcegraph.ClientResourcesOptions) (armresourcegraph.ClientResourcesResponse, error) {
	if query.Query != nil {
		f.query = *query.Query
	}
	f.managementGroups = nil
	for _, mg := range query.ManagementGroups {
		if mg != nil {
			f.managementGroups = append(f.managementGroups, *mg)
		}
	}
	if f.err != nil {
		return armresourcegraph.ClientResourcesResponse{}, f.err
	}
	return armresourcegraph.ClientResourcesResponse{
		QueryResponse: armresourcegraph.QueryResponse{Data: f.rows},
	}, nil
}

func responseError(status int, code string) error {
	return &azcore.ResponseError{StatusCode: status, ErrorCode: code}
}

func TestTagsFor(t *testing.T) {
	tests := []struct {
		name     string
		rows     []any
		wantName string
		want     map[string]string
	}{
		{"tags decoded", []any{map[string]any{
			"name": "web-01",
			"tags": map[string]any{"owner": "platform", "env": "prod"},
		}}, "web-01", map[string]string{"owner": "platform", "env": "prod"}},
		{"non-string values skipped", []any{map[string]any{
			"name": "web-01",
			"tags": map[string]any{"owner": "platform", "count": float64(3)},
		}}, "web-01", map[string]string{"owner": "platform"}},
		{"untagged resource", []any{map[string]any{"name": "web-01", "tags": nil}},
			"web-01", nil},
		{"nameless row falls back to the id", []any{map[string]any{
			"tags": map[string]any{"owner": "platform"},
		}}, vmID, map[string]string{"owner": "platform"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Client{graph: &fakeGraph{rows: tt.rows}}
			got, err := c.TagsFor(t.Context(), vmID)
			if err != nil {
				t.Fatalf("TagsFor() error = %v", err)
			}
			if got.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", got.Name, tt.wantName)
			}
			if !maps.Equal(got.Tags, tt.want) {
				t.Errorf("Tags = %v, want %v", got.Tags, tt.want)
			}
		})
	}
}

// The id lands in a single-quoted KQL literal, compared case-insensitively
// (=~) because ARM resource IDs are case-insensitive, with quotes escaped so
// no value can escape the literal.
func TestTagsForQuery(t *testing.T) {
	api := &fakeGraph{rows: []any{map[string]any{"name": "it"}}}
	c := &Client{graph: api}
	if _, err := c.TagsFor(t.Context(), "/subscriptions/it's"); err != nil {
		t.Fatalf("TagsFor() error = %v", err)
	}
	want := `Resources | where id =~ '/subscriptions/it\'s' | project name, tags`
	if api.query != want {
		t.Errorf("query = %q, want %q", api.query, want)
	}
	if len(api.managementGroups) != 0 {
		t.Errorf("managementGroups = %v, want none for the tenant-wide scope", api.managementGroups)
	}
}

func TestTagsForScopesToManagementGroup(t *testing.T) {
	api := &fakeGraph{rows: []any{map[string]any{"name": "it"}}}
	c := &Client{managementGroup: "platform-mg", graph: api}
	if _, err := c.TagsFor(t.Context(), vmID); err != nil {
		t.Fatalf("TagsFor() error = %v", err)
	}
	if len(api.managementGroups) != 1 || api.managementGroups[0] != "platform-mg" {
		t.Errorf("managementGroups = %v, want [platform-mg]", api.managementGroups)
	}
}

func TestTagsForErrors(t *testing.T) {
	tests := []struct {
		name     string
		api      *fakeGraph
		notFound bool
	}{
		{"no rows is final", &fakeGraph{}, true},
		{"invalid query is final", &fakeGraph{
			err: responseError(http.StatusBadRequest, "BadRequest")}, true},
		{"throttling is retryable", &fakeGraph{
			err: responseError(http.StatusTooManyRequests, "RateLimiting")}, false},
		{"authorization failure is retryable", &fakeGraph{
			err: responseError(http.StatusForbidden, "AuthorizationFailed")}, false},
		{"transport failure is retryable", &fakeGraph{err: errors.New("dial tcp: timeout")}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Client{graph: tt.api}
			_, err := c.TagsFor(t.Context(), vmID)
			if err == nil {
				t.Fatal("TagsFor() expected an error")
			}
			if errors.Is(err, ErrNotFound) != tt.notFound {
				t.Errorf("errors.Is(err, ErrNotFound) = %v, want %v (err %v)",
					!tt.notFound, tt.notFound, err)
			}
		})
	}
}

func TestVerify(t *testing.T) {
	tests := []struct {
		name string
		api  *fakeGraph
		ok   bool
	}{
		{"reachable scope", &fakeGraph{}, true},
		{"broken identity binding", &fakeGraph{
			err: responseError(http.StatusForbidden, "AuthorizationFailed")}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Client{managementGroup: "platform-mg", graph: tt.api}
			if err := c.verify(t.Context()); (err == nil) != tt.ok {
				t.Fatalf("verify() = %v, want ok=%v", err, tt.ok)
			}
			if tt.api.query == "" {
				t.Error("verify() must run a query")
			}
			if len(tt.api.managementGroups) != 1 {
				t.Errorf("verify() ran outside the configured scope: %v", tt.api.managementGroups)
			}
		})
	}
}

func TestEmptyIDIsNotFound(t *testing.T) {
	c := &Client{graph: &fakeGraph{}}
	if _, err := c.TagsFor(t.Context(), ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("TagsFor(\"\") = %v, want ErrNotFound", err)
	}
}
