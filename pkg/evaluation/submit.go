// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package evaluation

import "encoding/json"

// SubmissionVersion is the accepted Submission.Version value.
const SubmissionVersion = "v1"

// AuthInfo is GET /api/v1/auth/info: everything the client's login flow
// needs to run OIDC discovery and the PKCE authorization-code exchange, so a
// user configures nothing but the service URL.
type AuthInfo struct {
	// Issuer is the OIDC issuer URL (discovery is derived from it).
	Issuer string `json:"issuer"`
	// ClientID of the public (PKCE, secret-less) client the login uses.
	ClientID string `json:"clientID"`
	// Scopes to request; the client unions in openid and offline_access.
	Scopes []string `json:"scopes,omitempty"`
	// Mode is the server's auth mode ("oidc" or "none"). Mode none needs no
	// login at all.
	Mode string `json:"mode"`
}

// WorkspaceRef names a content-addressed workspace bundle.
type WorkspaceRef struct {
	// Digest is the hex sha256 of the deterministic gzip tarball.
	Digest string `json:"digest"`
	// SizeBytes of the tarball.
	SizeBytes int64 `json:"sizeBytes,omitempty"`
}

// HarnessOption is one acceptable harness for a unit, in preference order.
type HarnessOption struct {
	// Harness id ("claude", "codex", …).
	Harness string `json:"harness"`
	// ModelID is the harness-native model id the unit's model maps to.
	ModelID string `json:"modelID,omitempty"`
}

// JudgeSpec binds the in-pod LLM judge. V1 constraint: the judge model must
// be runnable by the unit's own harness — the client validates this before
// submitting.
type JudgeSpec struct {
	// Model is the canonical provider-qualified judge model id.
	Model string `json:"model"`
	// ModelID is its harness-native id on the unit's harness.
	ModelID string `json:"modelID,omitempty"`
}

// UnitSpec is one evaluation unit: a skill evaluated on one model at one
// tier. The triggers/evals spec documents travel in the workspace bundle
// (the evals/<skill>/ tree), not here — the pod loads them with its own
// loaders, keeping submissions small and bundles self-contained.
type UnitSpec struct {
	// Skill under evaluation and the plugin it belongs to.
	Skill  string `json:"skill"`
	Plugin string `json:"plugin,omitempty"`
	// Tier of the run: 1 = triggers, 2 = evals.
	Tier int `json:"tier"`
	// Model is the canonical provider-qualified model id.
	Model string `json:"model"`
	// Harnesses that can run the model, in preference order; the scheduler
	// launches the first one enabled in the runner fleet.
	Harnesses []HarnessOption `json:"harnesses"`
	// Workspace bundle the unit runs against.
	Workspace WorkspaceRef `json:"workspace"`
	// TimeoutMS bounds one agent run's wall clock, per tier semantics.
	TimeoutMS int64 `json:"timeoutMS,omitempty"`
	// MaxTurns per agent run (0 = the runner default).
	MaxTurns int `json:"maxTurns,omitempty"`
	// RunsPerQuery repeats each trigger query (tier 1 only).
	RunsPerQuery int `json:"runsPerQuery,omitempty"`
	// Jobs is the in-pod case concurrency (0 = the runner default).
	Jobs int `json:"jobs,omitempty"`
	// Baseline also runs each executed eval without the skill (tier 2).
	Baseline bool `json:"baseline,omitempty"`
	// Judge grades llm assertions (tier 2); nil skips the judge.
	Judge *JudgeSpec `json:"judge,omitempty"`
	// Cases is the case allowlist (trigger queries or eval ids); nil runs
	// all. The client computes --new/--failed/--modified selection locally
	// and encodes the outcome here.
	Cases []string `json:"cases,omitempty"`
	// PriorEntry is the client's existing results entry for this unit,
	// opaque to patchy. It seeds fingerprints, previous-run snapshots, and
	// baselines in-pod, so those behave exactly as they do locally.
	PriorEntry json.RawMessage `json:"priorEntry,omitempty"`
	// ClientVersion is the submitting evolve's version, recorded for skew
	// diagnostics (warned, never enforced).
	ClientVersion string `json:"clientVersion,omitempty"`
}

// Submission is POST /api/v1/evaluations.
type Submission struct {
	// Version of this contract; must be SubmissionVersion.
	Version string `json:"version"`
	// Units to run.
	Units []UnitSpec `json:"units"`
	// TTLSeconds overrides the server's retention of the finished
	// evaluation (0 keeps the server default).
	TTLSeconds int64 `json:"ttlSeconds,omitempty"`
}

// SubmissionResponse is the 201 body: the Evaluation name to monitor and the
// child unit names, index-ordered.
type SubmissionResponse struct {
	Name  string   `json:"name"`
	Units []string `json:"units"`
}

// SubmissionError is a non-2xx body. MissingWorkspaces (with a 412) lists
// digests the client must upload before resubmitting.
type SubmissionError struct {
	Error             string   `json:"error"`
	MissingWorkspaces []string `json:"missingWorkspaces,omitempty"`
}
