// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package source

import "context"

// Repo identifies a repository by owner and name. Patchy's only forge family
// is GitHub today, so a bare owner/name pair is unambiguous; a source that
// needs to name a host uses RepositoryRef instead.
type Repo struct {
	Owner string
	Name  string
}

// String returns "owner/name".
func (r Repo) String() string { return r.Owner + "/" + r.Name }

// RepositoryRef names a repository fully enough to act on, including the
// forge it lives on. It is what a context enhancer returns when it resolves
// the repository for a finding that arrived without one.
type RepositoryRef struct {
	// Provider is the forge family, e.g. "github".
	Provider string `json:"provider"`
	// Owner is the organization or user.
	Owner string `json:"owner,omitempty"`
	// Name is the repository name.
	Name string `json:"name,omitempty"`
	// URL is the full https URL. It supersedes Owner/Name when set — the
	// resolver knows its own host, and only a URL can express a self-hosted
	// forge.
	URL string `json:"url,omitempty"`
}

// CloudResource identifies a cloud resource a finding was raised against, for
// findings about infrastructure rather than repository code. The json tags are
// part of the public contract: it is embedded verbatim in agent handoff files.
type CloudResource struct {
	// Provider is the cloud platform, e.g. "google".
	Provider string `json:"provider"`
	// Name is the platform's canonical, unique resource identifier — for
	// Security Command Center, the finding's resourceName.
	Name string `json:"name"`
	// Type is the platform's resource type, e.g. "google.cloud.storage.Bucket".
	Type string `json:"type,omitempty"`
	// Project is the enclosing project, subscription, or account id.
	Project string `json:"project,omitempty"`
	// Location is the region or zone, when the platform reports one.
	Location string `json:"location,omitempty"`
	// DisplayName is the resource's human-facing name.
	DisplayName string `json:"display_name,omitempty"`
}

// Location is one place in the repository a finding points at. The json tags
// are part of the public contract: locations are embedded verbatim in issue
// manifests and agent handoff files.
type Location struct {
	// Path is repository-relative.
	Path      string `json:"path"`
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
	// Snippet is the offending source excerpt, when the tool provides one.
	Snippet string `json:"snippet,omitempty"`
}

// Finding is a source-agnostic security finding. The integration-controller
// keys accumulation on (scope, Source, primary advisory) — where scope is the
// repository for code findings and the cloud resource for infrastructure ones
// — and stamps the labels and issue content from these fields alone.
//
// A finding names a Repo, a CloudResource, or both. One with neither is
// rejected: there would be nothing to accumulate it against.
type Finding struct {
	// Source is the emitting Handler's ID, e.g. "ghas".
	Source string
	// Repo is the repository the finding was raised against. Zero on a cloud
	// finding, whose repository (if it has one at all) is resolved later by a
	// context enhancer.
	Repo Repo
	// CloudResource is the cloud resource the finding was raised against.
	// Nil on a code finding.
	CloudResource *CloudResource
	// AlertNumber is the source-native unique finding number (the
	// code-scanning alert number for GHAS). Ignored when AlertID is set.
	AlertNumber int
	// AlertID is the source-native identifier for tools that do not number
	// their findings — an SCC finding name, say. It supersedes AlertNumber.
	AlertID string
	// Advisories are the categorization identifiers (CWE/CVE/GHSA numbers),
	// most authoritative first: Advisories[0] is the accumulation key
	// (GHSA over CVE over CWE, per the source's judgement).
	Advisories []string
	// RuleID is the tool's rule identifier, e.g. a CodeQL query id.
	RuleID string
	// Title is a one-line human summary.
	Title string
	// Description is the full markdown help/description for the rule.
	Description string
	// Severity is the tool-reported severity, normalized by the handler to
	// low, medium, high, or critical.
	Severity string
	// HTMLURL links back to the finding in the source tool.
	HTMLURL string
	// Locations are the places the finding was raised, when available.
	Locations []Location
}

// Handler is the interface a finding source implements.
type Handler interface {
	// ID names the source; it becomes the security-source label value.
	ID() string
	// Findings normalizes one delivery into zero or more Findings. The event
	// type is the provider's own discriminator — a GitHub webhook event name,
	// or a constant for a provider whose endpoint carries only one kind. A
	// delivery the handler chooses to skip (an action it does not act on,
	// say) returns (nil, nil).
	Findings(ctx context.Context, eventType string, payload []byte) ([]Finding, error)
}

// VerdictKind is a triage decision worth telling the source about.
type VerdictKind string

// Verdict kinds.
const (
	// VerdictIgnore: the investigation judged the finding a false positive or
	// not exploitable. GHAS dismisses the alert; a cloud source mutes it.
	VerdictIgnore VerdictKind = "ignore"
)

// Verdict is the pipeline's decision, as told to the originating tool.
type Verdict struct {
	// Kind is the decision.
	Kind VerdictKind
	// Reason is the tool-facing reason code, where the tool has a vocabulary.
	Reason string
	// Comment is human-readable justification.
	Comment string
}

// AlertRef is one alert to act on, as recorded on the finding.
type AlertRef struct {
	// ID is the alert's identifier in the source system.
	ID string
	// Source is the handler that produced the alert. The pipeline groups
	// alerts by it and calls each source with only its own, so a Resolver can
	// treat an unparseable ID as bad data rather than as another tool's.
	Source string
	// URL is the alert in the source system, when it has one.
	URL string
}

// Resolver is the optional write-back capability of a source: recording the
// pipeline's triage verdict against the originating tool, so the finding does
// not sit open there after patchy has closed it here. GHAS dismisses the
// code-scanning alert; Security Command Center mutes the finding.
//
// It is deliberately separate from Handler. A source that only reads is a
// complete source — the pipeline type-asserts for Resolver and skips the
// write-back when it is absent, rather than forcing every handler to carry a
// no-op method.
type Resolver interface {
	// Resolve records the verdict against every alert. It must be idempotent:
	// the pipeline calls it once per phase entry, but retries are possible.
	Resolve(ctx context.Context, alerts []AlertRef, v Verdict) error
}
