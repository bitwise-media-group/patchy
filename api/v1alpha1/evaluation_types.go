// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EvaluationPhase is the lifecycle of one evaluation submission. It is a
// local enum, not part of the Finding phase taxonomy in transitions.go: the
// evaluation-controller is the single writer and derives the phase from child
// counts, so no transition table is needed. Expiry is deletion (TTL), not a
// phase.
// +kubebuilder:validation:Enum=Pending;Running;Complete;Failed
type EvaluationPhase string

// Evaluation phases.
const (
	// EvaluationPending: accepted, children not yet created.
	EvaluationPending EvaluationPhase = "Pending"
	// EvaluationRunning: children created, at least one not yet settled.
	EvaluationRunning EvaluationPhase = "Running"
	// EvaluationComplete: every unit settled and none failed.
	EvaluationComplete EvaluationPhase = "Complete"
	// EvaluationFailed: every unit settled and at least one failed.
	EvaluationFailed EvaluationPhase = "Failed"
)

// HarnessOption is one acceptable harness for a unit, in preference order:
// the scheduler launches the first option whose harness is enabled in the
// runner fleet.
type HarnessOption struct {
	// Harness id (e.g. "claude", "codex").
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	Harness string `json:"harness"`
	// ModelID is the harness-native model identifier the unit's model maps to
	// on this harness.
	// +optional
	// +kubebuilder:validation:MaxLength=256
	ModelID string `json:"modelID,omitempty"`
}

// WorkspaceRef names a content-addressed workspace bundle by its sha256
// digest. The digest is both identity and fetch capability: the agent pod
// downloads `<digest>.tar.gz` from the artifact endpoint and verifies it
// before extraction.
type WorkspaceRef struct {
	// Digest is the hex sha256 of the gzip tarball.
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{64}$`
	Digest string `json:"digest"`
	// SizeBytes of the tarball, as uploaded.
	// +optional
	// +kubebuilder:validation:Minimum=0
	SizeBytes int64 `json:"sizeBytes,omitempty"`
}

// UnitPlan is one evaluation unit: a skill evaluated on one model at one
// tier. The fields patchy schedules on (harness selection, workspace,
// timeout) are typed; everything else the in-pod client needs travels opaquely
// in ExecJSON — patchy never learns evaluation semantics.
type UnitPlan struct {
	// Skill under evaluation.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	Skill string `json:"skill"`
	// Tier of the run: 1 = triggers, 2 = evals.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=2
	Tier int32 `json:"tier"`
	// Model is the canonical provider-qualified model id the unit evaluates
	// (e.g. "anthropic/claude-sonnet-5").
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	Model string `json:"model"`
	// Harnesses that can run the model, in preference order. A unit whose
	// options are all disabled in the fleet settles as
	// Failed/HarnessUnavailable.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=8
	Harnesses []HarnessOption `json:"harnesses"`
	// Workspace bundle the unit runs against.
	Workspace WorkspaceRef `json:"workspace"`
	// TimeoutMilliseconds bounds the unit's wall clock (the Job deadline
	// clamps it controller-side).
	// +optional
	// +kubebuilder:validation:Minimum=0
	TimeoutMilliseconds int64 `json:"timeoutMilliseconds,omitempty"`
	// MaxTurns granted per agent run inside the unit.
	// +optional
	// +kubebuilder:validation:Minimum=0
	MaxTurns int32 `json:"maxTurns,omitempty"`
	// RunsPerQuery repeats each case this many times (tier 1).
	// +optional
	// +kubebuilder:validation:Minimum=0
	RunsPerQuery int32 `json:"runsPerQuery,omitempty"`
	// JudgeModel is the provider-qualified model the in-pod LLM judge runs
	// on; empty for no judge.
	// +optional
	// +kubebuilder:validation:MaxLength=256
	JudgeModel string `json:"judgeModel,omitempty"`
	// ExecJSON is the serialized remainder of the client's unit spec (cases
	// allowlist, prior results entry, client version, …), handed to the pod
	// verbatim. Opaque to patchy.
	// +optional
	// +kubebuilder:validation:MaxLength=131072
	ExecJSON string `json:"execJSON,omitempty"`
}

// EvaluationSpec is one submission. It is immutable — resubmission is a new
// Evaluation (CEL-enforced).
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec is immutable; submit a new Evaluation"
type EvaluationSpec struct {
	// Submitter is the authenticated username the API mapped from the
	// bearer token, recorded for display and audit.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	Submitter string `json:"submitter"`
	// Units to run, one child EvaluationUnit each.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=200
	Units []UnitPlan `json:"units"`
	// TTLSecondsAfterFinished overrides the controller's default retention
	// of the finished Evaluation and everything it owns.
	// +optional
	// +kubebuilder:validation:Minimum=0
	TTLSecondsAfterFinished *int32 `json:"ttlSecondsAfterFinished,omitempty"`
}

// EvaluationStatus aggregates the children. Written only by
// evaluation-controller; the phase is derived from unit counts.
type EvaluationStatus struct {
	// Phase of the submission.
	// +optional
	Phase EvaluationPhase `json:"phase,omitempty"`
	// Units is the number of child EvaluationUnits created.
	// +optional
	Units int32 `json:"units,omitempty"`
	// UnitsComplete counts children that settled Complete.
	// +optional
	UnitsComplete int32 `json:"unitsComplete,omitempty"`
	// UnitsFailed counts children that settled Failed.
	// +optional
	UnitsFailed int32 `json:"unitsFailed,omitempty"`
	// StartedAt is when the children were created.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// CompletedAt is when the last unit settled; the TTL clock starts here.
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`
	// Conditions of the submission (UnitsCreated, Complete).
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// ObservedGeneration is the last spec generation acted on.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=eval,categories=patchy
// +kubebuilder:printcolumn:name="Submitter",type=string,JSONPath=`.spec.submitter`
// +kubebuilder:printcolumn:name="Units",type=integer,JSONPath=`.status.units`
// +kubebuilder:printcolumn:name="Complete",type=integer,JSONPath=`.status.unitsComplete`
// +kubebuilder:printcolumn:name="Failed",type=integer,JSONPath=`.status.unitsFailed`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Evaluation is one remote evaluation submission: a batch of skill × model ×
// tier units an external client (evolve) uploaded for sandboxed execution.
// One immutable object per submission, owning one EvaluationUnit per unit;
// finished evaluations expire on a TTL.
type Evaluation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec EvaluationSpec `json:"spec"`
	// +optional
	Status EvaluationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// EvaluationList contains a list of Evaluation.
type EvaluationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Evaluation `json:"items"`
}
