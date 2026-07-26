// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// Label, annotation, and finalizer keys shared by every controller. The truth
// behind hashed label values (repository URLs, accumulation keys) always lives
// in the owning object's spec — labels exist only for selectors.
const (
	// LabelKeyHash carries the hex-encoded accumulation-key hash on a Finding.
	LabelKeyHash = "patchy.bitwisemedia.uk/key-hash"
	// LabelSource carries the source handler ID (e.g. github-code-scanning).
	LabelSource = "patchy.bitwisemedia.uk/source"
	// LabelIntegration names the Integration that ingested a Finding.
	LabelIntegration = "patchy.bitwisemedia.uk/integration"
	// LabelRepoHash carries the hex-encoded repository-URL hash.
	LabelRepoHash = "patchy.bitwisemedia.uk/repo-hash"
	// LabelSeverity mirrors spec.severity for selectors.
	LabelSeverity = "patchy.bitwisemedia.uk/severity"
	// LabelFinding names the owning Finding on children and agent Jobs.
	LabelFinding = "patchy.bitwisemedia.uk/finding"
	// LabelAttempt carries the attempt ordinal on Investigation/Remediation.
	LabelAttempt = "patchy.bitwisemedia.uk/attempt"
	// LabelOwner names the owning Investigation or Remediation on agent Jobs.
	LabelOwner = "patchy.bitwisemedia.uk/owner"
	// LabelRunKind discriminates agent Jobs ("investigation"/"remediation") so
	// the two job controllers sharing one namespace never touch each other's
	// Jobs.
	LabelRunKind = "patchy.bitwisemedia.uk/kind"
	// LabelHarness names the agent harness a Job's pod runs ("claude"/"codex"),
	// the selector the per-harness egress network policies match on so each
	// runner reaches only its own model API.
	LabelHarness = "patchy.bitwisemedia.uk/harness"
	// LabelScope carries the rollup scope type on FindingRollup objects.
	LabelScope = "patchy.bitwisemedia.uk/scope"

	// AnnotationRepo carries the true "owner/name" on Investigation and
	// Remediation children (label values cannot hold it), so the rollup can
	// attribute spend to the repository even after the Finding is gone.
	AnnotationRepo = "patchy.bitwisemedia.uk/repo"
)

// Finalizers. The rollup finalizers guarantee no object is deleted before its
// spend is aggregated into every scope (see FindingRollup); the jobs finalizer
// guarantees leftover Jobs/Secrets in the agents namespace are cleaned up.
const (
	FinalizerJobs             = "patchy.bitwisemedia.uk/jobs"
	FinalizerRollupTotal      = "patchy.bitwisemedia.uk/rollup-total"
	FinalizerRollupRepository = "patchy.bitwisemedia.uk/rollup-repository"
	FinalizerRollupHarness    = "patchy.bitwisemedia.uk/rollup-harness"
	FinalizerRollupModel      = "patchy.bitwisemedia.uk/rollup-model"
)

// LocalSecretReference names a Secret in the same namespace as the referring
// object.
type LocalSecretReference struct {
	// Name of the Secret.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// LocalObjectReference names an object of a known kind in the same namespace.
type LocalObjectReference struct {
	// Name of the referent.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// ObjectReference names an object in the same namespace and pins its UID, so
// a reference never silently rebinds to a recreated namesake (Finding names
// are deterministic and reused across TTL cycles).
type ObjectReference struct {
	// Name of the referent.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// UID of the referent at the time the reference was written.
	// +optional
	UID types.UID `json:"uid,omitempty"`
}

// JobReference locates an agent Job. Jobs live in the agents namespace, not
// the resource's own, so the namespace is explicit.
type JobReference struct {
	// Namespace the Job runs in.
	Namespace string `json:"namespace"`
	// Name of the Job.
	Name string `json:"name"`
	// UID of the Job.
	// +optional
	UID types.UID `json:"uid,omitempty"`
}

// Level grades severity and priority.
// +kubebuilder:validation:Enum=low;medium;high;critical
type Level string

// Severity/priority levels, lowest first.
const (
	LevelLow      Level = "low"
	LevelMedium   Level = "medium"
	LevelHigh     Level = "high"
	LevelCritical Level = "critical"
)

// Rating grades one investigation analysis dimension (exploitability,
// likelihood, impact). RatingNone is an assessed "not applicable / no risk";
// the empty string means the dimension was not assessed.
// +kubebuilder:validation:Enum=none;low;medium;high;critical
type Rating string

// Analysis ratings.
const (
	RatingNone     Rating = "none"
	RatingLow      Rating = "low"
	RatingMedium   Rating = "medium"
	RatingHigh     Rating = "high"
	RatingCritical Rating = "critical"
)

// Recommendation is the investigation verdict.
// +kubebuilder:validation:Enum=remediate;ignore;manual
type Recommendation string

// Investigation verdicts.
const (
	RecommendationRemediate Recommendation = "remediate"
	RecommendationIgnore    Recommendation = "ignore"
	RecommendationManual    Recommendation = "manual"
)

// HoldReason names why a finding stopped in AwaitingApproval instead of
// going straight to Queued. A hold can have several reasons at once, so they
// are reported as a set rather than folded into one flag.
// +kubebuilder:validation:Enum=breakingChangeAvailable;lowConfidence;estimateExceedsTurnCeiling;estimateExceedsTokenCeiling
type HoldReason string

// Approval-hold reasons.
const (
	// HoldBreakingChangeAvailable: a better-but-breaking fix exists, so a
	// human should choose between it and the backwards-compatible one.
	HoldBreakingChangeAvailable HoldReason = "breakingChangeAvailable"
	// HoldLowConfidence: the investigation's confidence fell below the
	// configured threshold.
	HoldLowConfidence HoldReason = "lowConfidence"
	// HoldEstimateExceedsTurnCeiling: the predicted turns exceed the ceiling,
	// so the run needs a human to authorize the larger budget.
	HoldEstimateExceedsTurnCeiling HoldReason = "estimateExceedsTurnCeiling"
	// HoldEstimateExceedsTokenCeiling: as above, for output tokens.
	HoldEstimateExceedsTokenCeiling HoldReason = "estimateExceedsTokenCeiling"
)

// RunKind discriminates the two agent run kinds.
// +kubebuilder:validation:Enum=investigation;remediation
type RunKind string

// Agent run kinds.
const (
	RunKindInvestigation RunKind = "investigation"
	RunKindRemediation   RunKind = "remediation"
)

// RunPhase is the lifecycle of one agent run (an Investigation or
// Remediation child).
// +kubebuilder:validation:Enum=Pending;Running;Complete;Failed
type RunPhase string

// Agent run phases.
const (
	RunPending  RunPhase = "Pending"
	RunRunning  RunPhase = "Running"
	RunComplete RunPhase = "Complete"
	RunFailed   RunPhase = "Failed"
)

// AgentEstimate is the investigation's prediction of what a remediation will
// need. It is advisory: it never reduces a run's budget. Its only operational
// effect is the approval gate — an estimate above the configured ceiling
// holds the finding in AwaitingApproval, and approving it grants the estimate
// (clamped to the hard cap). Everything else it does is reporting: estimate
// against granted against actual, and the skew averages in the rollups.
type AgentEstimate struct {
	// MaxTurns the investigation predicts the remediation will take.
	// +optional
	// +kubebuilder:validation:Minimum=0
	MaxTurns int32 `json:"maxTurns,omitempty"`
	// TokenBudget is the predicted output-token spend.
	// +optional
	// +kubebuilder:validation:Minimum=0
	TokenBudget int64 `json:"tokenBudget,omitempty"`
}

// AgentParameters bound one agent run: which model it uses and how much it
// may spend. MaxTurns/TokenBudget are what the run was actually GRANTED —
// the configured ceiling, or the investigation's estimate (clamped to the
// hard cap) when a human approved an over-ceiling estimate. They are never
// derived downward from the estimate: a run always gets at least the ceiling,
// whatever the investigation predicted, including when it predicted nothing.
type AgentParameters struct {
	// Model the harness runs, as a canonical provider-qualified id
	// (e.g. "anthropic/claude-sonnet-5").
	// +optional
	Model string `json:"model,omitempty"`
	// Harness that runs Model, resolved from the model by the remediation
	// spawner (the model's provider decides which agent-runner image and
	// credential the Job uses). Empty until resolution.
	// +optional
	Harness string `json:"harness,omitempty"`
	// MaxTurns granted to the run.
	// +optional
	// +kubebuilder:validation:Minimum=0
	MaxTurns int32 `json:"maxTurns,omitempty"`
	// TokenBudget granted to the run (output tokens; the runner kill switch).
	// +optional
	// +kubebuilder:validation:Minimum=0
	TokenBudget int64 `json:"tokenBudget,omitempty"`
	// Estimate the investigation made for this run, recorded alongside the
	// grant so the two can be compared after the fact.
	// +optional
	Estimate *AgentEstimate `json:"estimate,omitempty"`
}

// UsageSummary is one agent run's token and cost accounting. Zeroes mean the
// harness did not report the figure.
type UsageSummary struct {
	// InputTokens consumed.
	// +optional
	InputTokens int64 `json:"inputTokens,omitempty"`
	// OutputTokens produced.
	// +optional
	OutputTokens int64 `json:"outputTokens,omitempty"`
	// CacheReadTokens read from prompt cache.
	// +optional
	CacheReadTokens int64 `json:"cacheReadTokens,omitempty"`
	// CacheCreationTokens written to prompt cache.
	// +optional
	CacheCreationTokens int64 `json:"cacheCreationTokens,omitempty"`
	// CostUSD is the harness-reported cost as a decimal string (structural
	// schemas forbid floats).
	// +optional
	// +kubebuilder:validation:Pattern=`^[0-9]+(\.[0-9]{1,6})?$`
	CostUSD string `json:"costUSD,omitempty"`
}

// StageResult records how one agent stage ran, mirroring the agent envelope's
// stage block losslessly (sessionID is never truncated).
type StageResult struct {
	// Outcome is the envelope outcome vocabulary (ok, runtime_error, timeout,
	// budget_exceeded, report_missing, report_invalid, commit_failed,
	// changeset_too_large) plus "aborted" for a run killed with no envelope.
	Outcome string `json:"outcome"`
	// Harness that executed the stage.
	// +optional
	Harness string `json:"harness,omitempty"`
	// Model the stage ran on.
	// +optional
	Model string `json:"model,omitempty"`
	// SessionID of the harness session.
	// +optional
	SessionID string `json:"sessionID,omitempty"`
	// NumTurns the agent took.
	// +optional
	NumTurns int32 `json:"numTurns,omitempty"`
	// Usage accounting for the stage.
	// +optional
	Usage UsageSummary `json:"usage,omitempty"`
	// StartedAt is when the stage began.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// FinishedAt is when the stage ended.
	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`
	// ElapsedMilliseconds is the stage's wall-clock duration.
	// +optional
	ElapsedMilliseconds int64 `json:"elapsedMilliseconds,omitempty"`
	// Detail explains a non-ok outcome for humans.
	// +optional
	// +kubebuilder:validation:MaxLength=4096
	Detail string `json:"detail,omitempty"`
}

// ConfidencePattern validates a confidence decimal string in [0, 1] with up
// to four fractional digits.
const ConfidencePattern = `^(0(\.[0-9]{1,4})?|1(\.0{1,4})?)$`
