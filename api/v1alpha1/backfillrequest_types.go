// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// BackfillRequestPhase is the lifecycle of one backfill request. It is a
// local enum and is not part of the Finding phase taxonomy in
// transitions.go. The integration-controller is the single writer. Expiry is
// deletion (TTL) rather than a phase.
// +kubebuilder:validation:Enum=AwaitingApproval;Pending;Complete;Failed;Denied
type BackfillRequestPhase string

// Backfill request phases.
const (
	// BackfillAwaitingApproval: a request filed without a backfill grant
	// (via "requested") is waiting for an operator's spec.decision.
	BackfillAwaitingApproval BackfillRequestPhase = "AwaitingApproval"
	// BackfillPending: not walked yet, or the last walk failed and will be
	// retried (status.error says why).
	BackfillPending BackfillRequestPhase = "Pending"
	// BackfillComplete: the walk ran. status.error may still carry the last
	// alert that failed to ingest.
	BackfillComplete BackfillRequestPhase = "Complete"
	// BackfillFailed: the request will never run (the Integration is gone or
	// cannot list alerts), or its retries ran out.
	BackfillFailed BackfillRequestPhase = "Failed"
	// BackfillDenied: an operator denied the request; it never runs.
	BackfillDenied BackfillRequestPhase = "Denied"
)

// Terminal reports whether the phase is final.
func (p BackfillRequestPhase) Terminal() bool {
	return p == BackfillComplete || p == BackfillFailed || p == BackfillDenied
}

// BackfillVia records which grant authorized a backfill request.
// +kubebuilder:validation:Enum=backfill;maintained;requested
type BackfillVia string

// Backfill authorization paths.
const (
	// BackfillViaGrant: the requester holds the namespace-wide backfill verb
	// on integrations.
	BackfillViaGrant BackfillVia = "backfill"
	// BackfillViaMaintained: the requester holds backfill-maintained, and
	// GitHub reported them as admin or maintain on the repository. Only the
	// status server can create such a request, since only it can present the
	// linked GitHub identity.
	BackfillViaMaintained BackfillVia = "maintained"
	// BackfillViaRequested: the requester holds neither grant. The request
	// waits in AwaitingApproval until an operator holding the backfill verb
	// records spec.decision.
	BackfillViaRequested BackfillVia = "requested"
)

// BackfillVerdict is an operator's decision on a requested backfill.
// +kubebuilder:validation:Enum=Approved;Denied
type BackfillVerdict string

// Backfill verdicts.
const (
	BackfillApproved      BackfillVerdict = "Approved"
	BackfillDeniedVerdict BackfillVerdict = "Denied"
)

// BackfillDecision is an operator's one-time decision on a requested
// backfill. Setting it needs the backfill verb on integrations (the
// admission policy enforces this for direct writes).
type BackfillDecision struct {
	// Verdict is Approved or Denied.
	Verdict BackfillVerdict `json:"verdict"`
	// By is the deciding identity.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	By string `json:"by"`
	// At is when the decision was made.
	At metav1.Time `json:"at"`
	// Reason is an optional note shown to the requester.
	// +optional
	// +kubebuilder:validation:MaxLength=500
	Reason string `json:"reason,omitempty"`
}

// BackfillGitHubIdentity is the linked GitHub identity a maintained backfill
// was authorized with.
type BackfillGitHubIdentity struct {
	// Login is the GitHub login.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	Login string `json:"login"`
	// Role is the requester's role on the repository when the request was
	// authorized (admin or maintain).
	// +kubebuilder:validation:MaxLength=32
	Role string `json:"role"`
}

// BackfillRequestSpec is one request for a bounded list-alerts walk. Every
// field but decision is immutable, and decision may be set only once.
// +kubebuilder:validation:XValidation:rule="has(self.repositories) == has(oldSelf.repositories) && has(self.via) == has(oldSelf.via) && has(self.github) == has(oldSelf.github)",message="the request is immutable; create a new BackfillRequest"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.decision) || has(self.decision)",message="spec.decision may be set only once"
// +kubebuilder:validation:XValidation:rule="!has(self.decision) || (has(self.via) && self.via == \"requested\")",message="only a requested backfill takes a decision"
type BackfillRequestSpec struct {
	// IntegrationRef names the Integration whose credential walks the
	// provider's open alerts.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="the request is immutable; create a new BackfillRequest"
	IntegrationRef LocalObjectReference `json:"integrationRef"`
	// RequestedBy is the requesting identity, recorded for audit. The
	// admission policy requires it to match the creating user, except for
	// the status server, which records the signed-in user it acted for.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="the request is immutable; create a new BackfillRequest"
	RequestedBy string `json:"requestedBy"`
	// Repositories narrows the walk to repositories matching any entry: an
	// exact "owner/name", or an "owner/" prefix covering the whole owner.
	// Empty means the credential's full scope. A truncated walk is re-run
	// with a narrower prefix rather than paging on.
	// +optional
	// +kubebuilder:validation:MaxItems=50
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:MaxLength=200
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="the request is immutable; create a new BackfillRequest"
	Repositories []string `json:"repositories,omitempty"`
	// Via records which grant authorized the request; empty means the
	// backfill grant.
	// +optional
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="the request is immutable; create a new BackfillRequest"
	Via BackfillVia `json:"via,omitempty"`
	// GitHub is the linked GitHub identity of a maintained request.
	// +optional
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="the request is immutable; create a new BackfillRequest"
	GitHub *BackfillGitHubIdentity `json:"github,omitempty"`
	// Decision is an operator's verdict on a requested backfill.
	// +optional
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec.decision may be set only once"
	Decision *BackfillDecision `json:"decision,omitempty"`
}

// BackfillRequestStatus is the outcome of the request's walk.
type BackfillRequestStatus struct {
	// Phase is the request's lifecycle position.
	// +optional
	Phase BackfillRequestPhase `json:"phase,omitempty"`
	// Attempts counts the walks tried, failed ones included.
	// +optional
	Attempts int32 `json:"attempts,omitempty"`
	// LastAttemptAt is when the last walk started.
	// +optional
	LastAttemptAt *metav1.Time `json:"lastAttemptAt,omitempty"`
	// CompletedAt is when the request reached Complete or Failed; the TTL
	// counts from here.
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`
	// Listed counts the open alerts the walk saw.
	// +optional
	Listed int32 `json:"listed,omitempty"`
	// Ingested counts the alerts folded into findings; the rest were
	// filtered out by the repository prefixes or failed individually (see
	// error).
	// +optional
	Ingested int32 `json:"ingested,omitempty"`
	// Truncated marks a walk that hit the page budget before exhausting the
	// estate; request again with a narrower repository prefix to cover the
	// rest.
	// +optional
	Truncated bool `json:"truncated,omitempty"`
	// Error carries the last failure: why a pending walk will be retried,
	// why the request failed, or the last alert that would not ingest.
	// +optional
	Error string `json:"error,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=bfr,categories=patchy
// +kubebuilder:printcolumn:name="Integration",type=string,JSONPath=`.spec.integrationRef.name`
// +kubebuilder:printcolumn:name="By",type=string,JSONPath=`.spec.requestedBy`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Ingested",type=integer,JSONPath=`.status.ingested`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// BackfillRequest asks one Integration for one bounded walk of the
// provider's open alerts, ingesting the ones that predate webhook coverage.
// These are alerts raised before the App was installed on (or associated
// with) their repository, which no replay of the delivery log can surface.
// Ingestion is idempotent, so re-listing alerts that already have findings
// is safe.
//
// One object per request, created by the status page or the CLI and walked
// by the integration-controller one at a time. A request filed by someone
// without a backfill grant waits in AwaitingApproval for an operator's
// decision. The creation
// timestamp is the request time. Finished requests expire on a TTL; until
// then they are the record the status page's one-repository backfill
// cooldown reads.
type BackfillRequest struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec BackfillRequestSpec `json:"spec"`
	// +optional
	Status BackfillRequestStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// BackfillRequestList contains a list of BackfillRequest.
type BackfillRequestList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BackfillRequest `json:"items"`
}
