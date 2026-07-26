// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package gcpasset

import (
	"context"
	"errors"
	"fmt"
	"strings"

	asset "cloud.google.com/go/asset/apiv1"
	"cloud.google.com/go/asset/apiv1/assetpb"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

// ErrNotFound reports that Cloud Asset Inventory has no record of the
// resource. This is a permanent answer, not a transient one: the resource may
// have been deleted between the finding being raised and this lookup, or it
// may sit outside the configured scope. Callers use it to decide against
// retrying.
var ErrNotFound = errors.New("resource not found in cloud asset inventory")

// Labels are one resource's labels, plus enough identity to log about it.
type Labels struct {
	// Name is the resource's full name, as Security Command Center and Asset
	// Inventory both spell it.
	Name string
	// Labels are the resource's own labels.
	Labels map[string]string
}

// Client reads resource labels from Cloud Asset Inventory. There is no
// interface here on purpose: the consumer declares the one-method seam it
// needs and this satisfies it.
type Client struct {
	api   *asset.Client
	scope string
}

// New builds a client searching within scope, which must name an
// organization, folder, or project ("organizations/123", "folders/456",
// "projects/foo"). Credentials come from Application Default Credentials —
// in-cluster that means workload identity, so no key file exists anywhere.
func New(ctx context.Context, scope string) (*Client, error) {
	if err := ValidateScope(scope); err != nil {
		return nil, err
	}
	api, err := asset.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("gcpasset: build client: %w", err)
	}
	return &Client{api: api, scope: scope}, nil
}

// ValidateScope reports whether scope is one Asset Inventory accepts.
// Checked at construction so a typo fails at startup rather than on the first
// finding.
func ValidateScope(scope string) error {
	for _, prefix := range []string{"organizations/", "folders/", "projects/"} {
		if strings.HasPrefix(scope, prefix) && len(scope) > len(prefix) {
			return nil
		}
	}
	return fmt.Errorf(
		"gcpasset: scope %q must be organizations/<id>, folders/<id>, or projects/<id>", scope)
}

// Close releases the underlying connection.
func (c *Client) Close() error {
	if c.api == nil {
		return nil
	}
	return c.api.Close()
}

// LabelsFor returns the labels of one resource, named as Security Command
// Center names it.
//
// Errors are classified for the caller: ErrNotFound is final, everything else
// is worth retrying. That distinction is what lets the enhancer chain decide
// between advancing a finding and holding it for another try.
func (c *Client) LabelsFor(ctx context.Context, resourceName string) (*Labels, error) {
	if resourceName == "" {
		return nil, ErrNotFound
	}
	it := c.api.SearchAllResources(ctx, &assetpb.SearchAllResourcesRequest{
		Scope: c.scope,
		// An exact-name match: SCC's resourceName and Asset Inventory's name
		// are the same string, so no normalization is needed.
		Query: `name="` + resourceName + `"`,
		// Only the labels matter. Asking for less keeps the response small
		// and the permission this needs as narrow as it can be.
		ReadMask: &fieldmaskpb.FieldMask{Paths: []string{"name", "labels"}},
	})
	res, err := it.Next()
	switch {
	case errors.Is(err, iterator.Done):
		return nil, fmt.Errorf("%w: %s", ErrNotFound, resourceName)
	case err != nil:
		return nil, classify(resourceName, err)
	}
	return &Labels{Name: res.GetName(), Labels: res.GetLabels()}, nil
}

// classify wraps an Asset Inventory error, marking the permanent ones so a
// caller does not retry something that will never succeed. Everything else —
// including PermissionDenied, which is usually a workload-identity binding
// that has not propagated yet — stays retryable.
func classify(resourceName string, err error) error {
	switch status.Code(err) {
	case codes.NotFound, codes.InvalidArgument:
		return fmt.Errorf("%w: %s", ErrNotFound, resourceName)
	default:
		return fmt.Errorf("gcpasset: search %s: %w", resourceName, err)
	}
}
