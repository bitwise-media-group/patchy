// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package wiz

// The decoded shapes of the two documented automation-rule body templates.
// These mirror docs/integrations/wiz.md field for field — the template is the
// contract, so a field added here must be added there and vice versa. Only
// what patchy consumes is declared.

// envelope is the top-level body of either feed. Exactly one of Issue or
// Threat is set on a real delivery; a body with neither (Wiz's test delivery,
// or an automation rule pointed here by mistake) is treated as a ping.
type envelope struct {
	Trigger        trigger         `json:"trigger"`
	Issue          *issue          `json:"issue"`
	Threat         *threat         `json:"threat"`
	EntitySnapshot *entitySnapshot `json:"entitySnapshot"`
}

// trigger describes the automation rule that fired.
type trigger struct {
	// Source is what the rule listens to: ISSUE for the Issues feed,
	// THREAT_DETECTION for Defend.
	Source string `json:"source"`
	// Type is the lifecycle event: Created, Updated, Reopened, or Resolved.
	Type string `json:"type"`
	// RuleID and RuleName identify the automation rule itself — provenance,
	// not the security control that raised the issue.
	RuleID   string `json:"ruleId"`
	RuleName string `json:"ruleName"`
}

// issue is one Wiz Issue: a security control matched against a graph entity.
type issue struct {
	// ID is the Wiz issue id, the alert id on the Finding.
	ID string `json:"id"`
	// Status is OPEN, IN_PROGRESS, RESOLVED, or REJECTED.
	Status string `json:"status"`
	// Severity is CRITICAL, HIGH, MEDIUM, LOW, or INFORMATIONAL.
	Severity string `json:"severity"`
	// Created is when Wiz opened the issue.
	Created string `json:"created"`
	// Projects names the Wiz projects the entity belongs to.
	Projects string `json:"projects"`
	// Description is the issue prose, when the template carries one.
	Description string `json:"description"`
	// ResolutionRecommendation is Wiz's remediation guidance.
	ResolutionRecommendation string `json:"resolutionRecommendation"`
	// URL links to the issue in the Wiz console.
	URL string `json:"url"`
	// Control is the security control (policy rule) that raised the issue.
	Control control `json:"control"`
}

// control is the security control behind an issue — the issue's type, and
// what keys accumulation.
type control struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Severity    string `json:"severity"`
}

// entitySnapshot is the graph entity an issue was raised against, as it
// looked when the issue fired.
type entitySnapshot struct {
	// ID is the Wiz graph entity id.
	ID string `json:"id"`
	// Type is Wiz's abstract type (BUCKET, VIRTUAL_MACHINE, ...).
	Type string `json:"type"`
	// NativeType is the provider's own type name.
	NativeType string `json:"nativeType"`
	// Name is the resource's human-facing name.
	Name string `json:"name"`
	// CloudPlatform is GCP, AWS, Azure, or another Wiz platform value.
	CloudPlatform string `json:"cloudPlatform"`
	// CloudProviderURL links to the resource in its provider's console.
	CloudProviderURL string `json:"cloudProviderURL"`
	// ProviderID is the provider's own identifier for the resource — the
	// accumulation scope, after normalization for Google Cloud.
	ProviderID string `json:"providerId"`
	// Region the resource lives in.
	Region string `json:"region"`
	// ResourceGroupExternalID is the Azure resource group, when applicable.
	ResourceGroupExternalID string `json:"resourceGroupExternalId"`
	// SubscriptionExternalID is the cloud account: an Azure subscription id,
	// AWS account id, or Google Cloud project id.
	SubscriptionExternalID string `json:"subscriptionExternalId"`
	// SubscriptionName is the account's display name.
	SubscriptionName string `json:"subscriptionName"`
	// Tags are the provider tags/labels on the resource.
	Tags map[string]string `json:"tags"`
	// SubscriptionTags are the tags on the enclosing account.
	SubscriptionTags map[string]string `json:"subscriptionTags"`
}

// threat is one Wiz Defend detection rollup.
type threat struct {
	// ID is the Wiz threat id.
	ID string `json:"id"`
	// Name is the threat's title.
	Name string `json:"name"`
	// Description explains what was detected.
	Description string `json:"description"`
	// Severity is CRITICAL, HIGH, MEDIUM, LOW, or INFORMATIONAL.
	Severity string `json:"severity"`
	// Status is OPEN, IN_PROGRESS, RESOLVED, or REJECTED.
	Status string `json:"status"`
	// CreatedAt is when the threat was raised.
	CreatedAt string `json:"createdAt"`
	// RuleID and RuleName identify the detection rule that fired — the
	// threat's type, and what keys accumulation.
	RuleID   string `json:"ruleId"`
	RuleName string `json:"ruleName"`
	// CloudPlatform is the platform the detection fired on, when the threat
	// spans a single one; per-resource values override it.
	CloudPlatform string `json:"cloudPlatform"`
	// CloudAccounts are the external ids of the accounts involved.
	CloudAccounts []string `json:"cloudAccounts"`
	// MitreTactics and MitreTechniques map the detection onto ATT&CK.
	MitreTactics    []string `json:"mitreTactics"`
	MitreTechniques []string `json:"mitreTechniques"`
	// DetectionIDs are the individual detections rolled into the threat.
	DetectionIDs []string `json:"detectionIds"`
	// URL links to the threat in the Wiz console.
	URL string `json:"url"`
	// Actors are the identities that performed the detected activity.
	Actors []actor `json:"actors"`
	// Resources are the affected cloud resources; one Finding is created per
	// supported resource.
	Resources []threatResource `json:"resources"`
}

// actor is one identity involved in a detection.
type actor struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

// threatResource is one affected resource on a threat. A trimmed
// entitySnapshot: detections carry less resource context than issues.
type threatResource struct {
	ID                     string `json:"id"`
	Name                   string `json:"name"`
	Type                   string `json:"type"`
	NativeType             string `json:"nativeType"`
	ProviderID             string `json:"providerId"`
	Region                 string `json:"region"`
	CloudPlatform          string `json:"cloudPlatform"`
	SubscriptionExternalID string `json:"subscriptionExternalId"`
}
