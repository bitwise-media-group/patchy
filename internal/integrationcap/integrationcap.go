// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package integrationcap

import (
	"context"
	"errors"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
)

// Sentinel errors for integration selection.
var (
	// ErrNoIntegration: no enabled Integration provides the capability.
	ErrNoIntegration = errors.New("no integration provides the capability")
	// ErrAmbiguousIntegration: several Integrations provide the capability —
	// v1alpha1 requires exactly one per namespace.
	ErrAmbiguousIntegration = errors.New("multiple integrations provide the capability")
)

// Capability selects Integrations by what they provide.
type Capability func(*v1alpha1.Integration) bool

// Select returns the single Integration in namespace providing the
// capability (the v1alpha1 singleton rule).
func Select(
	ctx context.Context, r client.Reader, namespace string, has Capability,
) (*v1alpha1.Integration, error) {
	var list v1alpha1.IntegrationList
	if err := r.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("list integrations: %w", err)
	}
	var won []*v1alpha1.Integration
	for i := range list.Items {
		if has(&list.Items[i]) {
			won = append(won, &list.Items[i])
		}
	}
	switch len(won) {
	case 0:
		return nil, ErrNoIntegration
	case 1:
		return won[0], nil
	default:
		return nil, ErrAmbiguousIntegration
	}
}

// CloudAssetInventoryEnabled reports whether the Integration enables the
// Cloud Asset Inventory enhancer. It lives here rather than beside the
// receiver's ingestion predicates because its consumer is the
// context-controller, which must not import the integration-controller.
func CloudAssetInventoryEnabled(i *v1alpha1.Integration) bool {
	return !i.Spec.Suspend && i.Spec.GoogleCloud != nil &&
		i.Spec.GoogleCloud.CloudAssetInventory != nil &&
		i.Spec.GoogleCloud.CloudAssetInventory.Enabled
}
