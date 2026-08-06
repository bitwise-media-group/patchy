// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package generic

import "github.com/bitwise-media-group/patchy/pkg/source"

// Version is the contract version this package describes. Every exchanged
// body carries it; patchy rejects any other value.
const Version = "v1"

// Headers of the generic exchanges. Both directions sign the raw JSON body
// with the integration's shared secret and carry the result in
// SignatureHeader; the receiver recomputes the HMAC over the exact bytes
// received and compares in constant time.
const (
	// SignatureHeader carries "sha256=<hex of HMAC-SHA256(secret, body)>".
	SignatureHeader = "X-Patchy-Signature-256"
	// DeliveryHeader optionally carries a caller-chosen unique id for an
	// inbound delivery, used for deduplication. Absent, patchy derives one
	// from the body, so byte-identical redeliveries still dedup.
	DeliveryHeader = "X-Patchy-Delivery"
)

// Payload event discriminators.
const (
	// EventFindings delivers findings.
	EventFindings = "findings"
	// EventPing is a connectivity test; patchy answers 204 and ingests
	// nothing.
	EventPing = "ping"
)

// Payload is the inbound envelope an external process POSTs to
// /generic/<integration-name>/webhooks.
type Payload struct {
	// Version must be Version.
	Version string `json:"version"`
	// Event is EventFindings or EventPing.
	Event string `json:"event"`
	// Findings are the delivered findings; required (non-empty) for
	// EventFindings, ignored for EventPing.
	Findings []Finding `json:"findings,omitempty"`
}

// Repo identifies a repository by owner and name on the configured forge.
type Repo struct {
	// Owner is the organization or user.
	Owner string `json:"owner"`
	// Name is the repository name.
	Name string `json:"name"`
}

// Finding is one normalized security finding. The external process does its
// own normalization — patchy validates the shape and ingests, it does not
// interpret tool-native payloads.
//
// A finding names a Repo, a CloudResource, or both; one with neither is
// rejected, because there would be nothing to accumulate it against. It must
// also carry AlertID or a positive AlertNumber — the identity write-back and
// duplicate-merge key on.
type Finding struct {
	// Repo is the repository the finding was raised against; omit for a
	// cloud finding (an enhancer may resolve its repository later).
	Repo *Repo `json:"repo,omitempty"`
	// CloudResource is the cloud resource the finding was raised against;
	// omit for a code finding.
	CloudResource *source.CloudResource `json:"cloudResource,omitempty"`
	// AlertID is the finding's identifier in the source system. It
	// supersedes AlertNumber.
	AlertID string `json:"alertId,omitempty"`
	// AlertNumber is the source-native finding number, for tools that
	// number rather than name their findings.
	AlertNumber int `json:"alertNumber,omitempty"`
	// Advisories are the categorization identifiers (GHSA/CVE/CWE numbers,
	// or tool-native rule keys), most authoritative first: Advisories[0] is
	// the accumulation key. Empty, patchy falls back to a key derived from
	// RuleID (else AlertID/AlertNumber), so findings of the same rule fold.
	Advisories []string `json:"advisories,omitempty"`
	// RuleID is the tool's rule identifier.
	RuleID string `json:"ruleId,omitempty"`
	// Title is a one-line human summary. Required.
	Title string `json:"title"`
	// Description is the full markdown description; it becomes the tracking
	// issue body's finding section verbatim.
	Description string `json:"description,omitempty"`
	// Severity is low, medium, high, or critical. Required.
	Severity string `json:"severity"`
	// HTMLURL links back to the finding in the source tool.
	HTMLURL string `json:"htmlUrl,omitempty"`
	// Locations are the places the finding was raised, when available.
	Locations []source.Location `json:"locations,omitempty"`
}
