// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// IntegrationProvider identifies the external system an Integration talks to.
// +kubebuilder:validation:Enum=github;google-cloud;wiz
type IntegrationProvider string

// Integration providers.
const (
	IntegrationProviderGitHub      IntegrationProvider = "github"
	IntegrationProviderGoogleCloud IntegrationProvider = "google-cloud"
	IntegrationProviderWiz         IntegrationProvider = "wiz"
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
	// FallbackRepository ("owner/repo") receives the tracking issues of
	// findings that have no repository of their own — cloud findings whose
	// resource carries no ownership labels. Without it those findings are
	// visible only through kubectl and the status page. They still cannot be
	// remediated: the issue is a human surface, not a work tree.
	// +optional
	FallbackRepository string `json:"fallbackRepository,omitempty"`
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

// GoogleCloudSCCMute configures muting the Security Command Center finding
// when the investigation returns an "ignore" verdict — the cloud analogue of
// dismissing a code-scanning alert.
//
// RESERVED: the field is accepted and validated but not yet honoured. Muting
// needs securitycenter.findings.setMute, a Google Cloud write permission the
// integration-controller does not currently hold; the write-back seam
// (pkg/source.Resolver) is in place so enabling it is one implementation.
type GoogleCloudSCCMute struct {
	// Enabled turns the mute-on-ignore write-back on.
	Enabled bool `json:"enabled"`
}

// GoogleCloudSCC configures ingestion of Security Command Center findings.
// SCC has no webhook: a NotificationConfig publishes findings to a Pub/Sub
// topic and a push subscription forwards them to the receiver, which
// authenticates the OIDC token Pub/Sub signs rather than an HMAC — a push
// subscription cannot compute one over the body.
type GoogleCloudSCC struct {
	// Enabled turns the SCC ingestion capability on.
	Enabled bool `json:"enabled"`
	// Audience the push subscription's OIDC token must carry, as configured
	// on the subscription's oidcToken.audience.
	// +kubebuilder:validation:MinLength=1
	Audience string `json:"audience"`
	// ServiceAccount is the email address the push token must be issued for;
	// a token from any other identity is rejected.
	// +kubebuilder:validation:MinLength=1
	ServiceAccount string `json:"serviceAccount"`
	// MinSeverity drops notifications below this severity before they become
	// findings. The notification filter should do most of this work; this is
	// the backstop.
	// +optional
	// +kubebuilder:default=low
	MinSeverity Level `json:"minSeverity,omitempty"`
	// Organization is the numeric Google Cloud organization id, used to
	// compose Console links for findings that carry no externalUri.
	// +optional
	Organization string `json:"organization,omitempty"`
	// Mute configures the ignore-verdict write-back (reserved).
	// +optional
	Mute *GoogleCloudSCCMute `json:"mute,omitempty"`
}

// AssetLabelKeys overrides the resource label names the Cloud Asset Inventory
// enhancer reads a repository identity from. Empty fields keep the defaults
// (scm-repository-org, scm-repository-name, scm-repository-provider,
// scm-repository-url).
type AssetLabelKeys struct {
	// Org label carries the repository owner/organization.
	// +optional
	Org string `json:"org,omitempty"`
	// Name label carries the repository name.
	// +optional
	Name string `json:"name,omitempty"`
	// Provider label carries the forge kind (e.g. github).
	// +optional
	Provider string `json:"provider,omitempty"`
	// URL label carries a full repository URL, overriding the parts.
	// +optional
	URL string `json:"url,omitempty"`
}

// GoogleCloudAssetInventory enables the context enhancer that resolves the
// repository (and attributes) of any finding whose cloud resource lives on
// Google Cloud — whichever source ingested it — by reading the resource's
// ownership labels from Cloud Asset Inventory. Read-only: the
// context-controller authenticates by workload identity
// (roles/cloudasset.viewer) and holds no Secret.
type GoogleCloudAssetInventory struct {
	// Enabled turns the enhancer capability on.
	Enabled bool `json:"enabled"`
	// Scope bounds the asset search: an organization, folder, or project.
	// +kubebuilder:validation:Pattern=`^(organizations|folders|projects)/.+$`
	Scope string `json:"scope"`
	// RepositoryHost is the forge host repositories named by labels live on;
	// empty means github.com.
	// +optional
	RepositoryHost string `json:"repositoryHost,omitempty"`
	// Labels overrides the resource label names the enhancer reads.
	// +optional
	Labels *AssetLabelKeys `json:"labels,omitempty"`
}

// GoogleCloudIntegration is the google-cloud provider block. It carries no
// credential Secret: inbound findings authenticate themselves with a
// Pub/Sub-signed OIDC token, and the asset-inventory enhancer authenticates
// by workload identity. The two capabilities are independent — an integration
// may enhance findings from another source (e.g. Wiz) without ingesting from
// Security Command Center, or vice versa.
type GoogleCloudIntegration struct {
	// SecurityCommandCenter enables SCC finding ingestion.
	// +optional
	SecurityCommandCenter *GoogleCloudSCC `json:"securityCommandCenter,omitempty"`
	// CloudAssetInventory enables the ownership-label enhancer.
	// +optional
	CloudAssetInventory *GoogleCloudAssetInventory `json:"cloudAssetInventory,omitempty"`
}

// WizIssues configures ingestion of Wiz Issues — cloud misconfigurations and
// toxic combinations — delivered by a Wiz automation-rule webhook. The rule's
// action body must follow the template documented in docs/integrations/wiz.md;
// that template is the payload contract.
type WizIssues struct {
	// Enabled turns the Wiz Issues ingestion capability on.
	Enabled bool `json:"enabled"`
	// MinSeverity drops issues below this severity before they become
	// findings. Wiz INFORMATIONAL ranks below low, so the default floor
	// drops it.
	// +optional
	// +kubebuilder:default=low
	MinSeverity Level `json:"minSeverity,omitempty"`
}

// WizDefend configures ingestion of Wiz Defend threat detections, delivered
// by a Wiz automation-rule webhook using the documented body template.
type WizDefend struct {
	// Enabled turns the Wiz Defend ingestion capability on.
	Enabled bool `json:"enabled"`
	// MinSeverity drops detections below this severity before they become
	// findings.
	// +optional
	// +kubebuilder:default=low
	MinSeverity Level `json:"minSeverity,omitempty"`
}

// WizAPI configures the Wiz GraphQL API client used for write-back: on a
// dismissed finding the originating Wiz issue is rejected with a note. The
// block is optional — without it Wiz ingestion is one-way, exactly like SCC.
// Credentials are the Wiz service-account keys "clientId" + "clientSecret" in
// the integration's credential Secret.
type WizAPI struct {
	// Endpoint is the tenant GraphQL endpoint, e.g.
	// https://api.eu1.app.wiz.io/graphql.
	// +kubebuilder:validation:MinLength=1
	Endpoint string `json:"endpoint"`
	// TokenURL is the OAuth2 client-credentials token endpoint; empty means
	// https://auth.app.wiz.io/oauth/token.
	// +optional
	TokenURL string `json:"tokenURL,omitempty"`
}

// WizIntegration is the wiz provider block. Inbound deliveries carry the
// shared bearer token stored under key "webhookToken" in the credential
// Secret — a Wiz automation action sends a static header and cannot compute
// an HMAC over the body. Issues and Defend are independent capabilities that
// share the /wiz/webhooks path; the receiver discriminates by payload shape.
type WizIntegration struct {
	// Issues enables Wiz Issues ingestion.
	// +optional
	Issues *WizIssues `json:"issues,omitempty"`
	// Defend enables Wiz Defend threat-detection ingestion.
	// +optional
	Defend *WizDefend `json:"defend,omitempty"`
	// API enables write-back through the Wiz GraphQL API.
	// +optional
	API *WizAPI `json:"api,omitempty"`
}

// IntegrationSpec configures one external system. Exactly the provider block
// matching spec.provider must be set (CEL-enforced) — integrations are
// strongly typed, not generic.
// +kubebuilder:validation:XValidation:rule="(self.provider == 'github') == has(self.github) && (self.provider == 'google-cloud') == has(self.googleCloud) && (self.provider == 'wiz') == has(self.wiz)",message="exactly the provider block matching spec.provider must be set"
// +kubebuilder:validation:XValidation:rule="!(self.provider in ['github', 'wiz']) || has(self.secretRef)",message="spec.secretRef is required for the github and wiz providers"
type IntegrationSpec struct {
	// Provider is the external system type.
	Provider IntegrationProvider `json:"provider"`
	// SecretRef names the credential Secret. For github: either key "token"
	// (PAT, dev) or keys "appID" + "privateKey" (GitHub App), plus
	// "webhookSecret" for receiver HMAC validation. For wiz: key
	// "webhookToken" (the shared bearer token deliveries carry), plus keys
	// "clientId" + "clientSecret" when spec.wiz.api enables write-back.
	// Required for github and wiz, optional otherwise — a google-cloud
	// integration holds no credential, since Pub/Sub authenticates itself
	// with a signed OIDC token. It stays permitted for every provider so a
	// future capability that does need one (SCC mute-on-ignore) needs no
	// schema change.
	// +optional
	SecretRef *LocalSecretReference `json:"secretRef,omitempty"`
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
	// Reset requests the demo reset (stamped by the status page): the
	// controller permanently deletes every finding's tracking issue,
	// reopens the dismissed findings' code-scanning alerts, deletes every
	// pipeline resource, and drops the receiver's delivery dedup window so
	// redeliveries of already-handled GUIDs are ingested again. Issue
	// deletion requires admin-level repository permission on the
	// credential. Freshness against status.resetAt decides whether the
	// request is still actionable — the controller never clears spec.
	// +optional
	Reset *ActionRequest `json:"reset,omitempty"`
	// GitHub is the github provider block.
	// +optional
	GitHub *GitHubIntegration `json:"github,omitempty"`
	// GoogleCloud is the google-cloud provider block.
	// +optional
	GoogleCloud *GoogleCloudIntegration `json:"googleCloud,omitempty"`
	// Wiz is the wiz provider block.
	// +optional
	Wiz *WizIntegration `json:"wiz,omitempty"`
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
	// ResetAt echoes spec.reset.at once the receiver's dedup window has
	// been dropped; a spec.reset newer than this triggers another drop.
	// +optional
	ResetAt *metav1.Time `json:"resetAt,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:categories=patchy
// +kubebuilder:printcolumn:name="Provider",type=string,JSONPath=`.spec.provider`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Webhook",type=string,JSONPath=`.status.webhookPath`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Integration configures one external system patchy exchanges finding state
// with: scanners in (code-scanning alerts, Security Command Center, Wiz),
// context enhancers (Cloud Asset Inventory), tracking out (GitHub issues),
// and the human signals flowing back.
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
