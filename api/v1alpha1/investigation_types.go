// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Analysis is one investigation dimension: a rating plus a short
// justification. The full reasoning lives in the report markdown.
type Analysis struct {
	// Rating of the dimension.
	Rating Rating `json:"rating"`
	// Summary is the agent's short justification.
	// +optional
	// +kubebuilder:validation:MaxLength=4096
	Summary string `json:"summary,omitempty"`
}

// InvestigationSpec identifies one analysis attempt. It is immutable — a new
// attempt is a new Investigation (CEL-enforced).
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec is immutable; create a new Investigation for a new attempt"
type InvestigationSpec struct {
	// FindingRef is the owning Finding (UID-pinned).
	FindingRef ObjectReference `json:"findingRef"`
	// Attempt ordinal, 1-based.
	// +kubebuilder:validation:Minimum=1
	Attempt int32 `json:"attempt"`
	// RepositoryRef names the Repository artifact the agent works from; nil
	// for repository-less findings.
	// +optional
	RepositoryRef *LocalObjectReference `json:"repositoryRef,omitempty"`
	// Parameters bound this run (clamped controller-side).
	// +optional
	Parameters AgentParameters `json:"parameters,omitempty"`
}

// InvestigationStatus records the analysis results. Written only by
// investigation-controller.
type InvestigationStatus struct {
	// Phase of the run.
	// +optional
	Phase RunPhase `json:"phase,omitempty"`
	// JobRef locates the agent Job in the agents namespace.
	// +optional
	JobRef *JobReference `json:"jobRef,omitempty"`
	// Stage is the agent accounting for the run.
	// +optional
	Stage *StageResult `json:"stage,omitempty"`
	// Exploitability analysis: can the vulnerability actually be exercised?
	// +optional
	Exploitability *Analysis `json:"exploitability,omitempty"`
	// Likelihood analysis: how probable is exploitation in this deployment?
	// +optional
	Likelihood *Analysis `json:"likelihood,omitempty"`
	// Impact analysis: what is the blast radius if exploited?
	// +optional
	Impact *Analysis `json:"impact,omitempty"`
	// Recommendation is the verdict.
	// +optional
	Recommendation Recommendation `json:"recommendation,omitempty"`
	// Confidence decimal string in [0, 1].
	// +optional
	// +kubebuilder:validation:Pattern=`^(0(\.[0-9]{1,4})?|1(\.0{1,4})?)$`
	Confidence string `json:"confidence,omitempty"`
	// Severity as assessed by the agent (may differ from the scanner's).
	// +optional
	Severity Level `json:"severity,omitempty"`
	// Priority as assessed by the agent (display; scheduling priority is
	// computed controller-side).
	// +optional
	Priority Level `json:"priority,omitempty"`
	// AwaitApproval marks an approval hold: remediation waits for a human.
	// HoldReasons says why.
	// +optional
	AwaitApproval bool `json:"awaitApproval,omitempty"`
	// HoldReasons enumerates every reason the hold fired. Empty when
	// AwaitApproval is false.
	// +optional
	// +listType=set
	HoldReasons []HoldReason `json:"holdReasons,omitempty"`
	// RemediationParameters are the stage-2 parameters the investigation
	// suggested: the model to run, and (on Estimate) its prediction of the
	// turns and tokens the fix will need. The prediction is advisory — the
	// grant is resolved by the remediation spawner, never here.
	// +optional
	RemediationParameters *AgentParameters `json:"remediationParameters,omitempty"`
	// Report is the full analysis report markdown (single copy — the Finding
	// mirrors ratings only).
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
// +kubebuilder:resource:shortName=inv,categories=patchy
// +kubebuilder:printcolumn:name="Finding",type=string,JSONPath=`.spec.findingRef.name`
// +kubebuilder:printcolumn:name="Attempt",type=integer,JSONPath=`.spec.attempt`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Verdict",type=string,JSONPath=`.status.recommendation`
// +kubebuilder:printcolumn:name="Confidence",type=string,JSONPath=`.status.confidence`
// +kubebuilder:printcolumn:name="Exploitability",type=string,JSONPath=`.status.exploitability.rating`,priority=1
// +kubebuilder:printcolumn:name="Likelihood",type=string,JSONPath=`.status.likelihood.rating`,priority=1
// +kubebuilder:printcolumn:name="Impact",type=string,JSONPath=`.status.impact.rating`,priority=1
// +kubebuilder:printcolumn:name="Cost",type=string,JSONPath=`.status.stage.usage.costUSD`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Investigation is one analysis attempt on a Finding: an agent job assessing
// exploitability, likelihood, and impact, and recommending
// remediate/ignore/manual. One immutable object per attempt, owned by the
// Finding.
type Investigation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec InvestigationSpec `json:"spec"`
	// +optional
	Status InvestigationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// InvestigationList contains a list of Investigation.
type InvestigationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Investigation `json:"items"`
}
