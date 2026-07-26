// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ScopeType is a rollup dimension.
// +kubebuilder:validation:Enum=total;repository;harness;model
type ScopeType string

// Rollup scopes.
const (
	ScopeTotal      ScopeType = "total"
	ScopeRepository ScopeType = "repository"
	ScopeHarness    ScopeType = "harness"
	ScopeModel      ScopeType = "model"
)

// RollupScope identifies one dimension value. Rollups are sharded — one
// small object per scope value — because a single object holding a per-repo
// map would exceed etcd's object cap at estate scale (~15K repositories).
type RollupScope struct {
	// Type of the dimension.
	Type ScopeType `json:"type"`
	// Key is the dimension value ("" for total, "owner/repo", harness ID,
	// model ID). The sanitized form appears in labels; this field is truth.
	// +optional
	Key string `json:"key,omitempty"`
}

// FindingRollupSpec pins the scope; it is immutable.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec.scope is immutable"
type FindingRollupSpec struct {
	// Scope this rollup aggregates.
	Scope RollupScope `json:"scope"`
}

// StageAggregate accumulates one stage's runs within a scope. Averages and
// rates are computed client-side from sum ÷ count — merge-idempotent and
// exact.
type StageAggregate struct {
	// Runs counted.
	// +optional
	Runs int64 `json:"runs,omitempty"`
	// Succeeded runs (outcome ok; for remediation, success=true).
	// +optional
	Succeeded int64 `json:"succeeded,omitempty"`
	// Outcomes histogram (envelope outcome vocabulary + "aborted").
	// +optional
	Outcomes map[string]int64 `json:"outcomes,omitempty"`
	// InputTokens summed.
	// +optional
	InputTokens int64 `json:"inputTokens,omitempty"`
	// OutputTokens summed.
	// +optional
	OutputTokens int64 `json:"outputTokens,omitempty"`
	// CacheReadTokens summed.
	// +optional
	CacheReadTokens int64 `json:"cacheReadTokens,omitempty"`
	// CacheCreationTokens summed.
	// +optional
	CacheCreationTokens int64 `json:"cacheCreationTokens,omitempty"`
	// CostMicroUSD summed (micro-USD, exact integer arithmetic).
	// +optional
	CostMicroUSD int64 `json:"costMicroUSD,omitempty"`
	// ElapsedMilliseconds summed.
	// +optional
	ElapsedMilliseconds int64 `json:"elapsedMilliseconds,omitempty"`
	// Turns summed across every run counted here.
	// +optional
	Turns int64 `json:"turns,omitempty"`
	// Estimate accumulates how well the investigation predicted this stage's
	// cost. Absent for the investigation stage, which estimates nothing.
	// +optional
	Estimate *EstimateAggregate `json:"estimate,omitempty"`
}

// EstimateAggregate accumulates predicted-against-actual cost over the runs
// that carried an investigation estimate. The actual sums are matched — they
// cover exactly the runs the predicted sums cover, not every run in the
// StageAggregate — so predicted ÷ actual is a like-for-like ratio even when
// some runs (older objects, runs that died before reporting) have no
// estimate. Skew is computed client-side as actual ÷ predicted - 1.
type EstimateAggregate struct {
	// Runs that carried an estimate; the denominator for both averages.
	// +optional
	Runs int64 `json:"runs,omitempty"`
	// PredictedTurns summed over those runs.
	// +optional
	PredictedTurns int64 `json:"predictedTurns,omitempty"`
	// ActualTurns summed over those same runs.
	// +optional
	ActualTurns int64 `json:"actualTurns,omitempty"`
	// PredictedOutputTokens summed over those runs.
	// +optional
	PredictedOutputTokens int64 `json:"predictedOutputTokens,omitempty"`
	// ActualOutputTokens summed over those same runs.
	// +optional
	ActualOutputTokens int64 `json:"actualOutputTokens,omitempty"`
}

// RollupBucket accumulates finding-level counters within a scope. Total and
// repository scopes carry everything; harness and model scopes carry only
// the stages aggregates (a finding has no single harness/model owner).
type RollupBucket struct {
	// Findings counted (one per finding reaching a terminal phase, corrected
	// on revival).
	// +optional
	Findings int64 `json:"findings,omitempty"`
	// Phases histogram of terminal phases (remediated, failed, dismissed,
	// handedOff, deleted).
	// +optional
	Phases map[string]int64 `json:"phases,omitempty"`
	// Recommendations histogram of verdicts (remediate, ignore, manual).
	// +optional
	Recommendations map[string]int64 `json:"recommendations,omitempty"`
	// Attempts summed across findings.
	// +optional
	Attempts int64 `json:"attempts,omitempty"`
	// Stages aggregates per stage ("investigation", "remediation").
	// +optional
	Stages map[string]StageAggregate `json:"stages,omitempty"`
}

// MonthlyBucket is the slim global trend line (total scope only; ~150 bytes
// per month). Deleted findings cannot be re-bucketed later, so this is
// written at rollup time.
type MonthlyBucket struct {
	// Findings completed in the month.
	// +optional
	Findings int64 `json:"findings,omitempty"`
	// Runs executed in the month.
	// +optional
	Runs int64 `json:"runs,omitempty"`
	// CostMicroUSD spent in the month.
	// +optional
	CostMicroUSD int64 `json:"costMicroUSD,omitempty"`
}

// RollupSchemaVersion is the current bucket layout.
//
//	1 — the original counters.
//	2 — StageAggregate.Turns and StageAggregate.Estimate.
//
// Every bump so far has been additive, so a bucket written at an older
// version stays valid and is upgraded in place; the fields added by the bump
// simply carry no history. EstimateAggregate.Runs is the denominator that
// keeps that honest — it counts only the runs that actually contributed.
const RollupSchemaVersion int32 = 2

// FindingRollupStatus is the accumulated statistics. All data lives in
// status; the ledger makes application exactly-once per contributing object.
type FindingRollupStatus struct {
	// SchemaVersion of the bucket layout; see RollupSchemaVersion.
	// +optional
	SchemaVersion int32 `json:"schemaVersion,omitempty"`
	// FirstProcessed is the stats epoch for this scope.
	// +optional
	FirstProcessed *metav1.Time `json:"firstProcessed,omitempty"`
	// LastProcessed is the most recent aggregation.
	// +optional
	LastProcessed *metav1.Time `json:"lastProcessed,omitempty"`
	// Bucket is the all-time aggregate.
	// +optional
	Bucket RollupBucket `json:"bucket,omitempty"`
	// Monthly trend line keyed "2026-07" (total scope only).
	// +optional
	Monthly map[string]MonthlyBucket `json:"monthly,omitempty"`
	// Recent is the exactly-once ledger: the last 512 applied delta keys
	// ("i:<uid>", "r:<uid>", "f:<uid>:<seq>").
	// +optional
	// +kubebuilder:validation:MaxItems=512
	Recent []string `json:"recent,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=fr,categories=patchy
// +kubebuilder:printcolumn:name="Scope",type=string,JSONPath=`.spec.scope.type`
// +kubebuilder:printcolumn:name="Key",type=string,JSONPath=`.spec.scope.key`
// +kubebuilder:printcolumn:name="Findings",type=integer,JSONPath=`.status.bucket.findings`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// FindingRollup accumulates all-time statistics for one scope value (total,
// one repository, one harness, or one model). Rollups survive the TTL
// deletion of the findings they aggregate; they live only in etcd, so a
// cluster rebuild resets the stats epoch.
type FindingRollup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec FindingRollupSpec `json:"spec"`
	// +optional
	Status FindingRollupStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// FindingRollupList contains a list of FindingRollup.
type FindingRollupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FindingRollup `json:"items"`
}
