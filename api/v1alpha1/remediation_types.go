// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RemediationSpec identifies one remediation attempt. It is immutable, so a
// new attempt is a new Remediation (CEL-enforced).
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec is immutable; create a new Remediation for a new attempt"
type RemediationSpec struct {
	// FindingRef is the owning Finding (UID-pinned).
	FindingRef ObjectReference `json:"findingRef"`
	// InvestigationRef is the analysis this run executes.
	InvestigationRef ObjectReference `json:"investigationRef"`
	// RepositoryRef names the Repository artifact the agent works from (the
	// same SHA-pinned tree the investigation analysed).
	RepositoryRef LocalObjectReference `json:"repositoryRef"`
	// Attempt ordinal, 1-based.
	// +kubebuilder:validation:Minimum=1
	Attempt int32 `json:"attempt"`
	// Priority is the scheduling priority (0–100) computed from the
	// investigation results; the scheduler grants slots to the highest
	// pending priority first.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	Priority int32 `json:"priority"`
	// Parameters bound this run (clamped controller-side).
	// +optional
	Parameters AgentParameters `json:"parameters,omitempty"`
	// ApprovedBy is the human whose approval caused this run, when it exists
	// because of one.
	// +optional
	ApprovedBy string `json:"approvedBy,omitempty"`
	// Revival marks a remediate-only run reviving a handed-off finding.
	// +optional
	Revival bool `json:"revival,omitempty"`
}

// PullRequestRef is the opened pull request. Its lifecycle (merge/close)
// lives on the Finding. This object is immutable history after completion.
type PullRequestRef struct {
	// Number of the pull request.
	Number int64 `json:"number"`
	// URL of the pull request.
	// +optional
	URL string `json:"url,omitempty"`
}

// RemediationStatus records the remediation results. Written only by
// remediation-controller.
type RemediationStatus struct {
	// Phase of the run. Pending runs wait in the priority queue; the
	// scheduler's grant sets Running.
	// +optional
	Phase RunPhase `json:"phase,omitempty"`
	// GrantedAt is when the scheduler granted the run its slot.
	// +optional
	GrantedAt *metav1.Time `json:"grantedAt,omitempty"`
	// JobRef locates the agent Job in the agents namespace.
	// +optional
	JobRef *JobReference `json:"jobRef,omitempty"`
	// Stage is the agent accounting for the run.
	// +optional
	Stage *StageResult `json:"stage,omitempty"`
	// Success reports whether the agent produced a fix.
	// +optional
	Success bool `json:"success,omitempty"`
	// Confidence decimal string in [0, 1].
	// +optional
	// +kubebuilder:validation:Pattern=`^(0(\.[0-9]{1,4})?|1(\.0{1,4})?)$`
	Confidence string `json:"confidence,omitempty"`
	// Branch carrying the pushed fix.
	// +optional
	Branch string `json:"branch,omitempty"`
	// PushedCommit is the SHA of the pushed commit.
	// +optional
	PushedCommit string `json:"pushedCommit,omitempty"`
	// PullRequest opened for the fix.
	// +optional
	PullRequest *PullRequestRef `json:"pullRequest,omitempty"`
	// Report is the full remediation report markdown.
	// +optional
	// +kubebuilder:validation:MaxLength=65536
	Report string `json:"report,omitempty"`
	// Conditions of the run (Complete, RolledUp*).
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// ObservedGeneration is the last spec generation acted on.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=rem,categories=patchy
// +kubebuilder:printcolumn:name="Finding",type=string,JSONPath=`.spec.findingRef.name`
// +kubebuilder:printcolumn:name="Attempt",type=integer,JSONPath=`.spec.attempt`
// +kubebuilder:printcolumn:name="Priority",type=integer,JSONPath=`.spec.priority`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Success",type=boolean,JSONPath=`.status.success`
// +kubebuilder:printcolumn:name="Branch",type=string,JSONPath=`.status.branch`,priority=1
// +kubebuilder:printcolumn:name="Cost",type=string,JSONPath=`.status.stage.usage.costUSD`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Remediation is one remediation attempt on a Finding: an agent job that
// implements the fix the referenced Investigation recommended, whose
// changeset is pushed as a branch and opened as a pull request. One immutable
// object per attempt, owned by the Finding; the changeset itself never
// appears here.
type Remediation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec RemediationSpec `json:"spec"`
	// +optional
	Status RemediationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// RemediationList contains a list of Remediation.
type RemediationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Remediation `json:"items"`
}
