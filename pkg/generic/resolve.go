// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package generic

// AlertRef names one alert of the resolved finding, as the integration
// originally delivered it.
type AlertRef struct {
	// ID is the alert's identifier in the source system — the delivered
	// alertId, or the delivered alertNumber rendered as a string.
	ID string `json:"id"`
	// URL is the alert in the source system, when it was delivered.
	URL string `json:"url,omitempty"`
}

// Verdict is the pipeline's decision on the finding.
type Verdict struct {
	// Kind is the decision; "ignore" is the only kind today — the
	// investigation judged the finding a false positive or not exploitable.
	Kind string `json:"kind"`
	// Reason is a short machine-facing reason code.
	Reason string `json:"reason,omitempty"`
	// Comment is human-readable justification.
	Comment string `json:"comment,omitempty"`
}

// ResolveRequest is what patchy POSTs to the integration's resolver URL when
// a finding is dismissed, so the source system can close its own record. The
// process must treat it as idempotent: patchy retries on failure, so the
// same alerts may be resolved more than once. Any 2xx answer is success.
type ResolveRequest struct {
	// Version is the contract version, always Version.
	Version string `json:"version"`
	// Integration is the requesting Integration's name.
	Integration string `json:"integration"`
	// Alerts are the alerts to resolve.
	Alerts []AlertRef `json:"alerts"`
	// Verdict is the decision to record.
	Verdict Verdict `json:"verdict"`
}
