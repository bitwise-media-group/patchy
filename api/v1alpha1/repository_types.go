// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RepositoryRef pins what to fetch.
type RepositoryRef struct {
	// Branch to resolve; source-controller resolves it to a SHA exactly once
	// and pins status.resolvedSHA.
	// +optional
	Branch string `json:"branch,omitempty"`
}

// RepositorySpec asks source-controller to produce a SHA-pinned tarball of
// one repository.
type RepositorySpec struct {
	// URL is the normalized https URL of the repository to fetch.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec.url is immutable"
	URL string `json:"url"`
	// Ref selects what to fetch.
	// +optional
	Ref RepositoryRef `json:"ref,omitempty"`
}

// Artifact is the served source tarball: the forge's archive of the working
// tree at the resolved SHA, with no .git directory. The agent Job's init
// container creates a synthetic git base commit over it for the agent's
// commit and changeset flow.
type Artifact struct {
	// URL the artifact is served at (cluster-internal; the path embeds an
	// unguessable random id).
	URL string `json:"url"`
	// Digest is the sha256 of the tarball, verified by the fetching init
	// container.
	Digest string `json:"digest"`
	// SizeBytes of the tarball.
	// +optional
	SizeBytes int64 `json:"sizeBytes,omitempty"`
	// LastFetchedAt is when the tarball was downloaded from the forge.
	// +optional
	LastFetchedAt *metav1.Time `json:"lastFetchedAt,omitempty"`
}

// RepositoryStatus is the repository artifact's observed state.
type RepositoryStatus struct {
	// Conditions of the repository (Ready: artifact available; Stalled:
	// cannot fetch, or artifact over the size cap).
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// ObservedGeneration is the last spec generation acted on.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// ResolvedSHA is the commit the artifact is pinned to; investigation and
	// remediation both work on this tree, and it is the push base.
	// +optional
	ResolvedSHA string `json:"resolvedSHA,omitempty"`
	// Forge names the Forge whose credentials fetched the repository.
	// +optional
	Forge *LocalObjectReference `json:"forge,omitempty"`
	// Artifact is the served tarball.
	// +optional
	Artifact *Artifact `json:"artifact,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=repo,categories=patchy
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.spec.url`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="SHA",type=string,JSONPath=`.status.resolvedSHA`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Repository is a SHA-pinned source artifact: source-controller downloads
// the repository's tarball archive at the pinned SHA with Forge credentials
// and serves it to agent jobs, which fetch it credential-lessly. Owned by a
// Finding and garbage-collected with it.
type Repository struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec RepositorySpec `json:"spec"`
	// +optional
	Status RepositoryStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// RepositoryList contains a list of Repository.
type RepositoryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Repository `json:"items"`
}
