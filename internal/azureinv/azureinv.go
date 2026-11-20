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

// ErrNotFound reports that the inventory has no record of the resource. It is
// a permanent answer: the resource may have been deleted since the finding
// was raised, live in a subscription outside the configured scope, or not be
// indexed yet. Callers do not retry it.
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

// graphAPI is the part of the Resource Graph client the lookup uses. Tests
// replace it so they never dial Azure.
type graphAPI interface {
	Resources(ctx context.Context, query armresourcegraph.QueryRequest,
		options *armresourcegraph.ClientResourcesOptions) (armresourcegraph.ClientResourcesResponse, error)
}

// Client reads resource tags from Azure Resource Graph. This package declares
// no interface for it: the consumer declares the one-method interface it
// needs, and Client satisfies it.
type Client struct {
	managementGroup string
	graph           graphAPI
}

// New builds a client and verifies Resource Graph answers at all. A broken
// identity binding or a management group the identity cannot read fails here,
// at client build (a retryable hold), rather than silently per finding.
// Credentials come from the Azure default chain (Microsoft Entra Workload ID
// on AKS, workload identity federation elsewhere), so no key material exists
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

// Close releases nothing, since the SDK client holds no connection of its
// own. It exists so the enhancer can treat every cloud client alike.
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
// The ID is an exact identifier (compared case-insensitively, as ARM does),
// so there is no fallback search: a miss means the resource is unrecorded,
// not misspelled.
//
// ErrNotFound is final; every other error is worth retrying. The enhancer
// chain uses that to decide between advancing a finding and holding it for
// another try.
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
		Query: new(kql),
		Options: &armresourcegraph.QueryRequestOptions{
			ResultFormat: to.Ptr(armresourcegraph.ResultFormatObjectArray),
		},
	}
	if c.managementGroup != "" {
		req.ManagementGroups = []*string{new(c.managementGroup)}
	}
	out, err := c.graph.Resources(ctx, req, nil)
	return out.QueryResponse, err
}

// escape makes a value safe inside a single-quoted KQL string literal.
func escape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, "'", `\'`)
}

// normalizeTags converts the tags column to a map, or nil when there are no
// tags. Resource Graph returns a plain map, but the column is schemaless, so
// any other shape is tolerated and yields nil.
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
// does not retry something that will never succeed. Everything else stays
// retryable, including throttling and AuthorizationFailed (usually an
// identity binding that has not propagated yet).
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
