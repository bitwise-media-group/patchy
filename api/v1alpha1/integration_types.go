// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// IntegrationProvider identifies the external system an Integration talks to.
// +kubebuilder:validation:Enum=github
type IntegrationProvider string

// Integration providers.
const (
	IntegrationProviderGitHub IntegrationProvider = "github"
)

// GitHubIssues configures the tracking projection: findings are projected as
// GitHub issues, and the human signals on those issues (/approve comments,
// issue close/reopen, pull-request merge) flow back into Finding state.
type GitHubIssues struct {
	// Enabled turns the issues capability on.
	Enabled bool `json:"enabled"`
	// ApproveComment is the comment command that approves a held remediation.
	// +optional
	// +kubebuilder:default="/approve"
	ApproveComment string `json:"approveComment,omitempty"`
}

// GitHubCodeScanningAlerts configures scanner ingestion from GitHub code
// scanning (GHAS/CodeQL) webhooks, including alert dismissal on an "ignore"
// verdict.
type GitHubCodeScanningAlerts struct {
	// Enabled turns the code-scanning capability on.
	Enabled bool `json:"enabled"`
}

// GitHubRedelivery configures the failed-delivery sweep: on every
// reconcile interval the controller lists the App webhook's recent
// deliveries and asks GitHub to redeliver those that never got a 2xx —
// deliveries missed while the receiver was down or its queue was full.
// GitHub does not retry failed deliveries on its own; without the sweep
// they sit in the 30-day delivery log until redelivered by hand.
type GitHubRedelivery struct {
	// Enabled turns the sweep on. Requires App credentials — the delivery
	// log is App-scoped, so a PAT integration cannot sweep.
	Enabled bool `json:"enabled"`
	// Lookback bounds how far back each sweep scans the delivery log.
	// GitHub retains deliveries for 30 days.
	// +optional
	// +kubebuilder:default="24h"
	Lookback metav1.Duration `json:"lookback,omitempty"`
}

// GitHubIntegration is the github provider block: one GitHub App covering
// any combination of the capabilities.
type GitHubIntegration struct {
	// BaseURL points at a GitHub Enterprise Server host; empty means
	// github.com.
	// +optional
	BaseURL string `json:"baseURL,omitempty"`
	// Issues enables the tracking projection.
	// +optional
	Issues *GitHubIssues `json:"issues,omitempty"`
	// CodeScanningAlerts enables scanner ingestion.
	// +optional
	CodeScanningAlerts *GitHubCodeScanningAlerts `json:"codeScanningAlerts,omitempty"`
	// Redelivery enables the failed-delivery sweep.
	// +optional
	Redelivery *GitHubRedelivery `json:"redelivery,omitempty"`
}

// IntegrationSpec configures one external system. Exactly the provider block
// matching spec.provider must be set (CEL-enforced) — integrations are
// strongly typed, not generic.
// +kubebuilder:validation:XValidation:rule="(self.provider == 'github') == has(self.github)",message="exactly the provider block matching spec.provider must be set"
type IntegrationSpec struct {
	// Provider is the external system type.
	Provider IntegrationProvider `json:"provider"`
	// SecretRef names the credential Secret. For github: either key "token"
	// (PAT, dev) or keys "appID" + "privateKey" (GitHub App), plus
	// "webhookSecret" for receiver HMAC validation.
	SecretRef LocalSecretReference `json:"secretRef"`
	// Interval between credential revalidations.
	// +optional
	// +kubebuilder:default="10m"
	Interval metav1.Duration `json:"interval,omitempty"`
	// Suspend pauses reconciliation of this integration.
	// +optional
	Suspend bool `json:"suspend,omitempty"`
	// Replay requests one full redelivery of every webhook delivery in the
	// lookback window, including deliveries that already succeeded (the
	// receiver dedups; ingestion is idempotent). Freshness against
	// status.redelivery.replayedAt decides whether the request is still
	// actionable — the controller never clears spec.
	// +optional
	Replay *ActionRequest `json:"replay,omitempty"`
	// GitHub is the github provider block.
	// +optional
	GitHub *GitHubIntegration `json:"github,omitempty"`
}

// InstallationSummary counts one GitHub App installation — counts and
// accounts only, never a repository list (the estate is ~15K repositories).
type InstallationSummary struct {
	// ID of the installation.
	ID int64 `json:"id"`
	// Account the App is installed on.
	Account string `json:"account"`
	// Repositories the installation covers.
	// +optional
	Repositories int32 `json:"repositories,omitempty"`
}

// RateLimitStatus is an observability snapshot of the provider API quota.
type RateLimitStatus struct {
	// Remaining requests in the current window.
	Remaining int32 `json:"remaining"`
	// ResetAt is when the window resets.
	// +optional
	ResetAt *metav1.Time `json:"resetAt,omitempty"`
}

// RedeliveryStatus reports the last failed-delivery sweep.
type RedeliveryStatus struct {
	// LastSweepAt is when the last sweep ran.
	// +optional
	LastSweepAt *metav1.Time `json:"lastSweepAt,omitempty"`
	// Scanned counts the delivery attempts the last sweep inspected.
	// +optional
	Scanned int32 `json:"scanned,omitempty"`
	// Redelivered counts the redeliveries the last sweep requested.
	// +optional
	Redelivered int32 `json:"redelivered,omitempty"`
	// Truncated marks a sweep that hit the page cap before reaching the
	// lookback horizon; the oldest part of the window went unscanned.
	// +optional
	Truncated bool `json:"truncated,omitempty"`
	// Error carries the last sweep failure; empty when the sweep succeeded.
	// +optional
	Error string `json:"error,omitempty"`
	// ReplayedAt echoes spec.replay.at once that replay has run; a
	// spec.replay newer than this triggers another full replay.
	// +optional
	ReplayedAt *metav1.Time `json:"replayedAt,omitempty"`
}

// IntegrationStatus is the integration's observed state.
type IntegrationStatus struct {
	// Conditions of the integration (Ready: credential valid, system
	// reachable).
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// ObservedGeneration is the last spec generation acted on.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// WebhookPath is the receiver path deliveries must target
	// (e.g. /github/webhooks).
	// +optional
	WebhookPath string `json:"webhookPath,omitempty"`
	// Installations summarizes the App installations.
	// +optional
	Installations []InstallationSummary `json:"installations,omitempty"`
	// LastEventAt is when the receiver last accepted a delivery.
	// +optional
	LastEventAt *metav1.Time `json:"lastEventAt,omitempty"`
	// RateLimit is the last observed API quota.
	// +optional
	RateLimit *RateLimitStatus `json:"rateLimit,omitempty"`
	// Redelivery reports the last failed-delivery sweep.
	// +optional
	Redelivery *RedeliveryStatus `json:"redelivery,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:categories=patchy
// +kubebuilder:printcolumn:name="Provider",type=string,JSONPath=`.spec.provider`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Webhook",type=string,JSONPath=`.status.webhookPath`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Integration configures one external system patchy exchanges finding state
// with: scanners in (code-scanning alerts, wiz), tracking out (GitHub
// issues), and the human signals flowing back.
type Integration struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec IntegrationSpec `json:"spec"`
	// +optional
	Status IntegrationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// IntegrationList contains a list of Integration.
type IntegrationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Integration `json:"items"`
}
