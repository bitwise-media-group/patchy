// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// +kubebuilder:object:generate=true
// +groupName=patchy.bitwisemedia.uk

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// The api package deliberately depends only on apimachinery (no
// controller-runtime): it is imported by the e2e module and, eventually, the
// UI.
var (
	// GroupVersion is the group and version used to register these objects.
	GroupVersion = schema.GroupVersion{Group: "patchy.bitwisemedia.uk", Version: "v1alpha1"}

	// SchemeBuilder collects the functions that register this group-version's
	// types into a scheme.
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

// addKnownTypes registers every kind in the group with the scheme.
func addKnownTypes(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion,
		&Evaluation{}, &EvaluationList{},
		&EvaluationUnit{}, &EvaluationUnitList{},
		&Finding{}, &FindingList{},
		&FindingRollup{}, &FindingRollupList{},
		&Forge{}, &ForgeList{},
		&Integration{}, &IntegrationList{},
		&Investigation{}, &InvestigationList{},
		&Remediation{}, &RemediationList{},
		&Repository{}, &RepositoryList{},
	)
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
}
