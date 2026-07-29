// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package azureinv

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resourcegraph/armresourcegraph"
)

// ErrNotFound reports that the inventory has no record of the resource. This
// is a permanent answer, not a transient one: the resource may have been
// deleted between the finding being raised and this lookup, it may live in a
// subscription outside the configured scope, or it may not be indexed yet.
// Callers use it to decide against retrying.
var ErrNotFound = errors.New("resource not found in azure inventory")

// Tags are one resource's tags, plus enough identity to log about it.
type Tags struct {
	// Name is the resource's own name when the inventory reports one, else
	// its ARM resource ID.
	Name string
	// Tags are the resource's tags.
	Tags map[string]string
}

// Config bounds the Resource Graph query scope.
type Config struct {
	// ManagementGroup narrows the query to one management group; empty means
	// every subscription the ambient identity can read (the tenant, in
	// practice).
	ManagementGroup string
}

// graphAPI is the slice of the Resource Graph client the lookup uses; a seam
// for tests, which must not dial Azure.
type graphAPI interface {
	Resources(ctx context.Context, query armresourcegraph.QueryRequest,
		options *armresourcegraph.ClientResourcesOptions) (armresourcegraph.ClientResourcesResponse, error)
}

// Client reads resource tags from Azure Resource Graph. There is no interface
// here on purpose: the consumer declares the one-method seam it needs and
// this satisfies it.
type Client struct {
	managementGroup string
	graph           graphAPI
}

// New builds a client and verifies Resource Graph answers at all — a broken
// identity binding or a management group the identity cannot read fails here,
// at client build (a retryable hold), rather than silently per finding.
// Credentials come from the Azure default chain — Microsoft Entra Workload ID
// on AKS, workload identity federation elsewhere — so no key material exists
// anywhere.
func New(ctx context.Context, cfg Config) (*Client, error) {
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, fmt.Errorf("azureinv: load credentials: %w", err)
	}
	graph, err := armresourcegraph.NewClient(cred, nil)
	if err != nil {
		return nil, fmt.Errorf("azureinv: build resource graph client: %w", err)
	}
	c := &Client{managementGroup: cfg.ManagementGroup, graph: graph}
	if err := c.verify(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

// Close releases nothing — the SDK client holds no connection of its own —
// and exists so the enhancer can treat every cloud client alike.
func (c *Client) Close() error { return nil }

// verify runs a trivial query under the configured scope, so the credential
// and the scope are both exercised.
func (c *Client) verify(ctx context.Context) error {
	if _, err := c.run(ctx, "Resources | limit 1"); err != nil {
		return fmt.Errorf("azureinv: verify resource graph access: %w", err)
	}
	return nil
}

// TagsFor returns the tags of one resource, named by its ARM resource ID.
// The ID is an exact identifier — Resource Graph compares it
// case-insensitively, as ARM does — so there is no fallback search: a miss
// means the resource is unrecorded, not misspelled.
//
// Errors are classified for the caller: ErrNotFound is final, everything else
// is worth retrying. That distinction is what lets the enhancer chain decide
// between advancing a finding and holding it for another try.
func (c *Client) TagsFor(ctx context.Context, id string) (*Tags, error) {
	if id == "" {
		return nil, ErrNotFound
	}
	out, err := c.run(ctx, "Resources | where id =~ '"+escape(id)+"' | project name, tags")
	if err != nil {
		return nil, classify(id, err)
	}
	rows, _ := out.Data.([]any)
	if len(rows) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	row, ok := rows[0].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("azureinv: lookup %s: decode result: unexpected row shape %T", id, rows[0])
	}
	name, _ := row["name"].(string)
	if name == "" {
		name = id
	}
	return &Tags{Name: name, Tags: normalizeTags(row["tags"])}, nil
}

// run executes one KQL query, scoped to the management group when one is
// configured.
func (c *Client) run(ctx context.Context, kql string) (armresourcegraph.QueryResponse, error) {
	req := armresourcegraph.QueryRequest{
		Query: to.Ptr(kql),
		Options: &armresourcegraph.QueryRequestOptions{
			ResultFormat: to.Ptr(armresourcegraph.ResultFormatObjectArray),
		},
	}
	if c.managementGroup != "" {
		req.ManagementGroups = []*string{to.Ptr(c.managementGroup)}
	}
	out, err := c.graph.Resources(ctx, req, nil)
	return out.QueryResponse, err
}

// escape makes a value safe inside a single-quoted KQL string literal.
func escape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, "'", `\'`)
}

// normalizeTags accepts the tags column defensively — Resource Graph spells
// it as a plain map, but the column is schemaless — and returns a map.
func normalizeTags(v any) map[string]string {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	tags := map[string]string{}
	for k, val := range m {
		if s, ok := val.(string); ok {
			tags[k] = s
		}
	}
	if len(tags) == 0 {
		return nil
	}
	return tags
}

// classify wraps an inventory error, marking the permanent ones so a caller
// does not retry something that will never succeed. Everything else —
// including throttling and AuthorizationFailed, which is usually an identity
// binding that has not propagated yet — stays retryable.
func classify(id string, err error) error {
	var resp *azcore.ResponseError
	if errors.As(err, &resp) && resp.StatusCode == http.StatusBadRequest {
		// The query is generated and escaped; the only way it can be invalid
		// is the identifier itself. Retrying the same ID cannot go
		// differently.
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return fmt.Errorf("azureinv: lookup %s: %w", id, err)
}
