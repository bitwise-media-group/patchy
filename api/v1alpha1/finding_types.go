// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RepositoryType identifies the forge family a repository lives on.
// +kubebuilder:validation:Enum=github
type RepositoryType string

// Repository types.
const (
	RepositoryTypeGitHub RepositoryType = "github"
)

// FindingRepository locates the repository a finding belongs to. It is nil on
// findings (e.g. cloud findings) whose repository could not be identified —
// those can never reach the Queued/Remediating phases.
type FindingRepository struct {
	// Type of forge the repository lives on.
	Type RepositoryType `json:"type"`
	// URL is the normalized https URL of the repository.
	// +kubebuilder:validation:MinLength=1
	URL string `json:"url"`
	// Name is the human form ("owner/repo"), for display.
	// +optional
	Name string `json:"name,omitempty"`
	// DefaultBranch of the repository at ingest time.
	// +optional
	DefaultBranch string `json:"defaultBranch,omitempty"`
}

// Location is one source location an alert points at.
type Location struct {
	// Path of the file, repo-relative.
	Path string `json:"path"`
	// StartLine of the flagged region (1-based).
	// +optional
	StartLine int32 `json:"startLine,omitempty"`
	// EndLine of the flagged region.
	// +optional
	EndLine int32 `json:"endLine,omitempty"`
	// Snippet is the flagged source excerpt.
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	Snippet string `json:"snippet,omitempty"`
}

// Alert is one scanner alert folded into the finding during accumulation.
// IDs are strings — GHAS alert numbers render as decimal, other scanners use
// opaque identifiers.
type Alert struct {
	// ID of the alert in the source system.
	// +kubebuilder:validation:MinLength=1
	ID string `json:"id"`
	// URL of the alert in the source system.
	// +optional
	URL string `json:"url,omitempty"`
	// Locations the alert points at.
	// +optional
	// +kubebuilder:validation:MaxItems=8
	Locations []Location `json:"locations,omitempty"`
}

// RelationshipType classifies an edge between two findings.
// +kubebuilder:validation:Enum=duplicate-of;successor-of;related-to
type RelationshipType string

// Finding relationships.
const (
	// RelationshipDuplicateOf: From was merged into To and deleted.
	RelationshipDuplicateOf RelationshipType = "duplicate-of"
	// RelationshipSuccessorOf: From is the next accumulation generation of To.
	RelationshipSuccessorOf RelationshipType = "successor-of"
	// RelationshipRelatedTo is a free-form association (human- or
	// machine-written).
	RelationshipRelatedTo RelationshipType = "related-to"
)

// RelatedFinding is a directed edge between two findings, stored identically
// on both endpoints (one of from/to is the holder's own name). Names may
// dangle after the other endpoint's TTL deletion — consumers tolerate that.
type RelatedFinding struct {
	// From is the edge's source Finding name.
	From string `json:"from"`
	// To is the edge's target Finding name.
	To string `json:"to"`
	// Relationship classifies the edge.
	Relationship RelationshipType `json:"relationship"`
}

// Approval records a human /approve on the tracking item.
type Approval struct {
	// By is the approving login.
	By string `json:"by"`
	// At is when the approval was received.
	At metav1.Time `json:"at"`
	// Note is optional free text following the approve command.
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	Note string `json:"note,omitempty"`
}

// FindingSpec is the finding to triage. integration-controller writes it at
// ingest and during accumulation; humans may set suspend and add related-to
// edges — nothing else has a second writer.
type FindingSpec struct {
	// IntegrationRef names the Integration that ingested this finding.
	IntegrationRef LocalObjectReference `json:"integrationRef"`
	// TrackingRef names the Integration that projects this finding to its
	// tracking system, denormalized at creation (stable if the ingesting
	// integration later retargets).
	// +optional
	TrackingRef *LocalObjectReference `json:"trackingRef,omitempty"`
	// Source is the source handler ID (e.g. github-code-scanning).
	// +kubebuilder:validation:MinLength=1
	Source string `json:"source"`
	// Repository the finding belongs to; nil when none could be identified.
	// +optional
	Repository *FindingRepository `json:"repository,omitempty"`
	// Advisories identify the vulnerability, most authoritative first
	// (GHSA > CVE > CWE); [0] is the accumulation key.
	// +kubebuilder:validation:MinItems=1
	Advisories []string `json:"advisories"`
	// RuleID is the scanner rule that fired.
	// +optional
	RuleID string `json:"ruleID,omitempty"`
	// Title is the human summary line.
	// +optional
	Title string `json:"title,omitempty"`
	// Description is the scanner's full description, truncated at ingest.
	// +optional
	// +kubebuilder:validation:MaxLength=65536
	Description string `json:"description,omitempty"`
	// Severity assigned by the scanner.
	// +optional
	Severity Level `json:"severity,omitempty"`
	// Alerts folded into this finding during accumulation.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	Alerts []Alert `json:"alerts,omitempty"`
	// OverflowAlerts counts alerts dropped past the cap (total observed =
	// len(alerts) + overflowAlerts).
	// +optional
	OverflowAlerts int32 `json:"overflowAlerts,omitempty"`
	// Related is the finding's relationship edges.
	// +optional
	// +kubebuilder:validation:MaxItems=32
	Related []RelatedFinding `json:"related,omitempty"`
	// Suspend pauses pipeline progress for this finding (human-written).
	// +optional
	Suspend bool `json:"suspend,omitempty"`
	// Approval is the recorded human /approve, written by
	// integration-controller from the tracking system's comment webhook.
	// +optional
	Approval *Approval `json:"approval,omitempty"`
}

// PhaseTime records when the finding entered a phase (append-only).
type PhaseTime struct {
	// Phase entered.
	Phase Phase `json:"phase"`
	// At is the entry time.
	At metav1.Time `json:"at"`
}

// TrackingStatus links the finding to its projected tracking item.
type TrackingStatus struct {
	// Integration that owns the projection.
	Integration string `json:"integration"`
	// IssueNumber of the tracking item.
	// +optional
	IssueNumber int64 `json:"issueNumber,omitempty"`
	// URL of the tracking item.
	// +optional
	URL string `json:"url,omitempty"`
	// State of the tracking item (open/closed).
	// +optional
	State string `json:"state,omitempty"`
}

// Enrichment is one enhancer's contribution, written by context-controller
// and projected as a tracking comment by integration-controller.
type Enrichment struct {
	// Enhancer is the enhancer ID.
	Enhancer string `json:"enhancer"`
	// Owners are code-owner logins the enhancer identified, in preference
	// order.
	// +optional
	Owners []string `json:"owners,omitempty"`
	// Markdown is the enhancer's comment body.
	// +optional
	// +kubebuilder:validation:MaxLength=16384
	Markdown string `json:"markdown,omitempty"`
	// AppliedAt is when the enrichment was recorded.
	AppliedAt metav1.Time `json:"appliedAt"`
}

// InvestigationSummary mirrors the latest Investigation child onto the
// Finding so the UI list view needs only a Finding watch. Reports and
// per-dimension summaries live on the child only.
type InvestigationSummary struct {
	// Name of the Investigation child.
	Name string `json:"name"`
	// Attempt ordinal.
	Attempt int32 `json:"attempt"`
	// Outcome of the stage (envelope vocabulary).
	// +optional
	Outcome string `json:"outcome,omitempty"`
	// Recommendation verdict.
	// +optional
	Recommendation Recommendation `json:"recommendation,omitempty"`
	// Confidence decimal string in [0, 1].
	// +optional
	// +kubebuilder:validation:Pattern=`^(0(\.[0-9]{1,4})?|1(\.0{1,4})?)$`
	Confidence string `json:"confidence,omitempty"`
	// Exploitability rating.
	// +optional
	Exploitability Rating `json:"exploitability,omitempty"`
	// Likelihood rating.
	// +optional
	Likelihood Rating `json:"likelihood,omitempty"`
	// Impact rating.
	// +optional
	Impact Rating `json:"impact,omitempty"`
	// AwaitApproval marks the breaking-change hold.
	// +optional
	AwaitApproval bool `json:"awaitApproval,omitempty"`
	// CompletedAt is when the investigation finished.
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`
}

// RemediationSummary mirrors the latest Remediation child onto the Finding.
type RemediationSummary struct {
	// Name of the Remediation child.
	Name string `json:"name"`
	// Attempt ordinal.
	Attempt int32 `json:"attempt"`
	// Outcome of the stage (envelope vocabulary).
	// +optional
	Outcome string `json:"outcome,omitempty"`
	// Success reports whether the agent produced a fix.
	// +optional
	Success bool `json:"success,omitempty"`
	// Branch carrying the fix.
	// +optional
	Branch string `json:"branch,omitempty"`
	// CompletedAt is when the remediation finished.
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`
}

// PullRequestStatus is the lifecycle of the remediation pull request.
// remediation-controller writes number/url at open; integration-controller
// owns state/mergedAt from PR webhooks.
type PullRequestStatus struct {
	// Number of the pull request.
	Number int64 `json:"number"`
	// URL of the pull request.
	// +optional
	URL string `json:"url,omitempty"`
	// State of the pull request.
	// +optional
	// +kubebuilder:validation:Enum=open;merged;closed
	State string `json:"state,omitempty"`
	// MergedAt is when the pull request merged.
	// +optional
	MergedAt *metav1.Time `json:"mergedAt,omitempty"`
}

// AttemptCounts tallies agent runs per stage.
type AttemptCounts struct {
	// Investigation attempts so far.
	// +optional
	Investigation int32 `json:"investigation,omitempty"`
	// Remediation attempts so far.
	// +optional
	Remediation int32 `json:"remediation,omitempty"`
}

// ActiveRun points at the child currently holding the run lease (the lease
// itself is the deterministic child-CR create).
type ActiveRun struct {
	// Kind of the active run.
	Kind RunKind `json:"kind"`
	// Name of the active child.
	Name string `json:"name"`
}

// FindingStatus is the finding's observed state. Field writers are noted per
// field; every field has exactly one writer component.
type FindingStatus struct {
	// Phase of the lifecycle (see transitions.go for edges and writers).
	// +optional
	Phase Phase `json:"phase,omitempty"`
	// PhaseTimes is the append-only phase entry log.
	// +optional
	PhaseTimes []PhaseTime `json:"phaseTimes,omitempty"`
	// Conditions of the finding.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// ObservedGeneration is the last spec generation acted on.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// FirstObservedAt is when the first alert arrived (integration).
	// +optional
	FirstObservedAt *metav1.Time `json:"firstObservedAt,omitempty"`
	// AccumulateUntil closes the accumulation window (integration).
	// +optional
	AccumulateUntil *metav1.Time `json:"accumulateUntil,omitempty"`
	// Tracking links the projected tracking item (integration).
	// +optional
	Tracking *TrackingStatus `json:"tracking,omitempty"`
	// Owners are the humans responsible for the finding (context).
	// +optional
	Owners []string `json:"owners,omitempty"`
	// Enrichments are the enhancer contributions (context).
	// +optional
	// +kubebuilder:validation:MaxItems=8
	Enrichments []Enrichment `json:"enrichments,omitempty"`
	// Priority is the scheduling priority derived from investigation results
	// (investigation) — the remediation queue's sort dimension.
	// +optional
	Priority Level `json:"priority,omitempty"`
	// Investigation summarizes the latest Investigation child (investigation).
	// +optional
	Investigation *InvestigationSummary `json:"investigation,omitempty"`
	// Remediation summarizes the latest Remediation child (remediation).
	// +optional
	Remediation *RemediationSummary `json:"remediation,omitempty"`
	// PullRequest is the remediation PR (remediation opens; integration owns
	// state/mergedAt).
	// +optional
	PullRequest *PullRequestStatus `json:"pullRequest,omitempty"`
	// Attempts tallies agent runs (respective controllers).
	// +optional
	Attempts AttemptCounts `json:"attempts,omitempty"`
	// ActiveRun points at the child currently running (respective
	// controllers).
	// +optional
	ActiveRun *ActiveRun `json:"activeRun,omitempty"`
	// Forge names the resolved Forge, for observability.
	// +optional
	Forge *LocalObjectReference `json:"forge,omitempty"`
	// LastFailureReason explains the most recent failed edge.
	// +optional
	// +kubebuilder:validation:MaxLength=4096
	LastFailureReason string `json:"lastFailureReason,omitempty"`
	// CompletedAt is set on terminal-phase entry and cleared on revival; the
	// TTL contract is completedAt + TTL.
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=fnd,categories=patchy
// +kubebuilder:printcolumn:name="Repo",type=string,JSONPath=`.spec.repository.name`
// +kubebuilder:printcolumn:name="Severity",type=string,JSONPath=`.spec.severity`
// +kubebuilder:printcolumn:name="Priority",type=string,JSONPath=`.status.priority`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Verdict",type=string,JSONPath=`.status.investigation.recommendation`
// +kubebuilder:printcolumn:name="Issue",type=string,JSONPath=`.status.tracking.url`,priority=1
// +kubebuilder:printcolumn:name="PR",type=string,JSONPath=`.status.pullRequest.url`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Finding is the authoritative state machine for one security finding. The
// tracking item (e.g. a GitHub issue) is a one-way human-facing projection of
// this resource.
type Finding struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec FindingSpec `json:"spec"`
	// +optional
	Status FindingStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// FindingList contains a list of Finding.
type FindingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Finding `json:"items"`
}
