// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// UnitFailureReason names why an EvaluationUnit settled Failed without a
// trusted result. The client maps HarnessUnavailable to a skip and
// WorkspaceLost to a re-upload-and-resubmit prompt.
// +kubebuilder:validation:Enum=WorkspaceLost;HarnessUnavailable;JobFailed;Aborted;ResultTooLarge
type UnitFailureReason string

// Unit failure reasons.
const (
	// UnitWorkspaceLost: the workspace bundle is no longer in the artifact
	// store (retention sweep); re-upload and resubmit.
	UnitWorkspaceLost UnitFailureReason = "WorkspaceLost"
	// UnitHarnessUnavailable: no harness in the unit's preference list is
	// enabled in the runner fleet.
	UnitHarnessUnavailable UnitFailureReason = "HarnessUnavailable"
	// UnitJobFailed: the agent Job died without emitting a result event.
	UnitJobFailed UnitFailureReason = "JobFailed"
	// UnitAborted: the evaluation was cancelled while the unit ran.
	UnitAborted UnitFailureReason = "Aborted"
	// UnitResultTooLarge: the pod's result payload exceeded the wire bound
	// and could not be stored.
	UnitResultTooLarge UnitFailureReason = "ResultTooLarge"
)

// CaseSummary is one case's pass/fail, compact enough for a status list. The
// full-fidelity result lives in the results ConfigMap.
type CaseSummary struct {
	// ID of the case.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	ID string `json:"id"`
	// Passed reports the graded outcome.
	Passed bool `json:"passed"`
}

// EvaluationUnitSpec identifies one unit of its parent Evaluation. It is
// immutable and self-contained — launch never re-reads the parent
// (CEL-enforced).
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec is immutable"
type EvaluationUnitSpec struct {
	// EvaluationRef is the owning Evaluation (UID-pinned).
	EvaluationRef ObjectReference `json:"evaluationRef"`
	// Index of the unit within the submission, 0-based.
	// +kubebuilder:validation:Minimum=0
	Index int32 `json:"index"`
	// Unit is the plan, copied down from the parent spec.
	Unit UnitPlan `json:"unit"`
}

// EvaluationUnitStatus records how the unit ran. Written only by
// evaluation-controller.
type EvaluationUnitStatus struct {
	// Phase of the run.
	// +optional
	Phase RunPhase `json:"phase,omitempty"`
	// JobRef locates the agent Job in the agents namespace.
	// +optional
	JobRef *JobReference `json:"jobRef,omitempty"`
	// Harness the scheduler resolved at launch: the first enabled option
	// from the unit's preference list.
	// +optional
	Harness string `json:"harness,omitempty"`
	// Reason a Failed unit failed.
	// +optional
	Reason UnitFailureReason `json:"reason,omitempty"`
	// Detail explains a failure for humans.
	// +optional
	// +kubebuilder:validation:MaxLength=4096
	Detail string `json:"detail,omitempty"`
	// Cases are the per-case outcomes, bounded; the results ConfigMap holds
	// the full record.
	// +optional
	// +kubebuilder:validation:MaxItems=256
	Cases []CaseSummary `json:"cases,omitempty"`
	// CasesPassed counts cases that passed.
	// +optional
	CasesPassed int32 `json:"casesPassed,omitempty"`
	// CasesFailed counts cases that failed.
	// +optional
	CasesFailed int32 `json:"casesFailed,omitempty"`
	// CasesErrored counts cases that errored before grading.
	// +optional
	CasesErrored int32 `json:"casesErrored,omitempty"`
	// Usage accounting for the unit, summed over its agent runs.
	// +optional
	Usage UsageSummary `json:"usage,omitempty"`
	// StartedAt is when the Job launched.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// FinishedAt is when the unit settled.
	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`
	// ElapsedMilliseconds is the unit's wall-clock duration as reported by
	// the pod.
	// +optional
	ElapsedMilliseconds int64 `json:"elapsedMilliseconds,omitempty"`
	// ResultsRef locates the results ConfigMap holding the full-fidelity
	// result entry the client reassembles locally.
	// +optional
	ResultsRef *TranscriptRef `json:"resultsRef,omitempty"`
	// Conditions of the run (Complete).
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// ObservedGeneration is the last spec generation acted on.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=evu,categories=patchy
// +kubebuilder:printcolumn:name="Evaluation",type=string,JSONPath=`.spec.evaluationRef.name`
// +kubebuilder:printcolumn:name="Skill",type=string,JSONPath=`.spec.unit.skill`
// +kubebuilder:printcolumn:name="Tier",type=integer,JSONPath=`.spec.unit.tier`
// +kubebuilder:printcolumn:name="Model",type=string,JSONPath=`.spec.unit.model`
// +kubebuilder:printcolumn:name="Harness",type=string,JSONPath=`.status.harness`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Passed",type=integer,JSONPath=`.status.casesPassed`,priority=1
// +kubebuilder:printcolumn:name="Failed",type=integer,JSONPath=`.status.casesFailed`,priority=1
// +kubebuilder:printcolumn:name="Cost",type=string,JSONPath=`.status.usage.costUSD`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// EvaluationUnit is one unit of an Evaluation: a skill evaluated on one
// model at one tier inside a sandboxed agent Job. One immutable object per
// unit, owned by the Evaluation; bounded summaries live here, the full
// result in a per-unit ConfigMap.
type EvaluationUnit struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec EvaluationUnitSpec `json:"spec"`
	// +optional
	Status EvaluationUnitStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// EvaluationUnitList contains a list of EvaluationUnit.
type EvaluationUnitList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []EvaluationUnit `json:"items"`
}
