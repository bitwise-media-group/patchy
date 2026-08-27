// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ForgeProvider identifies the forge family a Forge authenticates against.
// +kubebuilder:validation:Enum=github
type ForgeProvider string

// Forge providers.
const (
	ForgeProviderGitHub ForgeProvider = "github"
)

// ForgeSpec configures repository credentials for one forge, scoped by
// optional org and repository filters. Matching (in internal/forge): the
// repository URL's host must equal the forge host, then the org allowlist,
// then the repository regexes; when several Forges match, the more
// constrained spec wins (orgs set beats unset, then repositories set beats
// unset) and a remaining tie is a configuration error surfaced on the
// Finding.
type ForgeSpec struct {
	// Provider is the forge family.
	Provider ForgeProvider `json:"provider"`
	// BaseURL is the forge host; empty means the provider's public host
	// (github.com).
	// +optional
	BaseURL string `json:"baseURL,omitempty"`
	// Proxy routes this forge's GitHub traffic through an HTTP/HTTPS forward
	// proxy, overriding the process proxy environment for this forge.
	// +optional
	Proxy *ProxyConfig `json:"proxy,omitempty"`
	// SecretRef names the credential Secret: key "token" (PAT, dev) or keys
	// "appID" + "privateKey" (GitHub App). With spec.proxy set, optional keys
	// "proxyUsername" + "proxyPassword" carry the proxy's basic-auth
	// credentials.
	SecretRef LocalSecretReference `json:"secretRef"`
	// Orgs restricts this forge to the listed organizations; empty means all.
	// +optional
	Orgs []string `json:"orgs,omitempty"`
	// Repositories restricts this forge to repositories whose "owner/name"
	// matches any of the listed RE2 regular expressions; empty means all.
	// +optional
	Repositories []string `json:"repositories,omitempty"`
	// Interval between credential revalidations.
	// +optional
	// +kubebuilder:default="10m"
	Interval metav1.Duration `json:"interval,omitempty"`
	// Suspend pauses reconciliation of this forge.
	// +optional
	Suspend bool `json:"suspend,omitempty"`
}

// ForgeStatus is the forge's observed state.
type ForgeStatus struct {
	// Conditions of the forge (Ready: credential valid).
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// ObservedGeneration is the last spec generation acted on.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Installations summarizes the App installations (github App auth only).
	// +optional
	Installations []InstallationSummary `json:"installations,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:categories=patchy
// +kubebuilder:printcolumn:name="Provider",type=string,JSONPath=`.spec.provider`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Forge holds repository credentials: source-controller resolves a Finding's
// repository to a Forge for read-only clone tokens; remediation-controller
// resolves the same Forge for scoped write tokens (branch push, pull
// request). Both go through the shared internal/forge package.
type Forge struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ForgeSpec `json:"spec"`
	// +optional
	Status ForgeStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ForgeList contains a list of Forge.
type ForgeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Forge `json:"items"`
}
