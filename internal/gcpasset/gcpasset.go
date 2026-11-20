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
// resource. It is a permanent answer: the resource may have been deleted since
// the finding was raised, or sit outside the configured scope. Callers do not
// retry it.
var ErrNotFound = errors.New("resource not found in cloud asset inventory")

// Labels are one resource's labels, plus enough identity to log about it.
type Labels struct {
	// Name is the resource's full name, as Security Command Center and Asset
	// Inventory both spell it.
	Name string
	// Labels are the resource's own labels.
	Labels map[string]string
}

// Client reads resource labels from Cloud Asset Inventory. This package
// declares no interface for it: the consumer declares the one-method
// interface it needs, and Client satisfies it.
type Client struct {
	api   *asset.Client
	scope string
}

// New builds a client searching within scope, which must name an
// organization, folder, or project ("organizations/123", "folders/456",
// "projects/foo"). Credentials come from Application Default Credentials. In
// cluster that means workload identity, so no key file exists anywhere.
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

// ValidateScope reports whether scope is one Asset Inventory accepts. It runs
// at construction so a typo fails at startup rather than on the first
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
// Center names it. When the exact name misses and the caller supplied a
// display name, one fallback search by display name runs. It covers sources
// (Wiz) whose identifiers cannot always be rewritten into Asset Inventory's
// form. The fallback requires a unique hit: two resources with the same
// display name are ambiguous, and guessing an owner would be worse than
// handing the finding to a human.
//
// ErrNotFound is final; every other error is worth retrying. The enhancer
// chain uses that to decide between advancing a finding and holding it for
// another try.
func (c *Client) LabelsFor(ctx context.Context, resourceName, displayName string) (*Labels, error) {
	if resourceName == "" {
		return nil, ErrNotFound
	}
	// An exact-name match first: SCC's resourceName and Asset Inventory's
	// name are the same string, so no normalization is needed.
	labels, err := c.search(ctx, `name="`+resourceName+`"`, resourceName, false)
	if err == nil || !errors.Is(err, ErrNotFound) || displayName == "" {
		return labels, err
	}
	return c.search(ctx, `displayName="`+displayName+`"`, resourceName, true)
}

// search runs one Asset Inventory query and returns its single hit.
func (c *Client) search(ctx context.Context, query, resourceName string, unique bool) (*Labels, error) {
	it := c.api.SearchAllResources(ctx, &assetpb.SearchAllResourcesRequest{
		Scope: c.scope,
		Query: query,
		// Only the labels matter. A narrow mask keeps the response small and
		// the required permission narrow.
		ReadMask: &fieldmaskpb.FieldMask{Paths: []string{"name", "labels"}},
	})
	res, err := it.Next()
	switch {
	case errors.Is(err, iterator.Done):
		return nil, fmt.Errorf("%w: %s", ErrNotFound, resourceName)
	case err != nil:
		return nil, classify(resourceName, err)
	}
	if unique {
		if _, err := it.Next(); !errors.Is(err, iterator.Done) {
			// A second hit (or an error hiding one) makes the answer unsafe.
			return nil, fmt.Errorf("%w: %s (display name is ambiguous)", ErrNotFound, resourceName)
		}
	}
	return &Labels{Name: res.GetName(), Labels: res.GetLabels()}, nil
}

// classify wraps an Asset Inventory error, marking the permanent ones so a
// caller does not retry something that will never succeed. Everything else
// stays retryable, including PermissionDenied (usually a workload-identity
// binding that has not propagated yet).
func classify(resourceName string, err error) error {
	switch status.Code(err) {
	case codes.NotFound, codes.InvalidArgument:
		return fmt.Errorf("%w: %s", ErrNotFound, resourceName)
	default:
		return fmt.Errorf("gcpasset: search %s: %w", resourceName, err)
	}
}
