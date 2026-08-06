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

// AWSResourceTagsEnabled reports whether the Integration enables the AWS
// resource-tags enhancer. It lives here for the same reason as
// CloudAssetInventoryEnabled: its consumer is the context-controller.
func AWSResourceTagsEnabled(i *v1alpha1.Integration) bool {
	return !i.Spec.Suspend && i.Spec.AWS != nil &&
		i.Spec.AWS.ResourceTags != nil &&
		i.Spec.AWS.ResourceTags.Enabled
}

// AzureResourceTagsEnabled reports whether the Integration enables the Azure
// resource-tags enhancer. It lives here for the same reason as
// CloudAssetInventoryEnabled: its consumer is the context-controller.
func AzureResourceTagsEnabled(i *v1alpha1.Integration) bool {
	return !i.Spec.Suspend && i.Spec.Azure != nil &&
		i.Spec.Azure.ResourceTags != nil &&
		i.Spec.Azure.ResourceTags.Enabled
}

// GenericEnhanceEnabled reports whether the Integration enables the external
// generic enhancer. It lives here for the same reason as
// CloudAssetInventoryEnabled: its consumer is the context-controller. Unlike
// every other capability, generic is exempt from the Select singleton rule —
// any number of generic integrations enhance side by side, so consumers List
// with this predicate rather than Select with it.
func GenericEnhanceEnabled(i *v1alpha1.Integration) bool {
	return !i.Spec.Suspend && i.Spec.Provider == v1alpha1.IntegrationProviderGeneric &&
		i.Spec.Generic != nil && i.Spec.Generic.Enhance != nil &&
		i.Spec.Generic.Enhance.Enabled
}
