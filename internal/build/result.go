// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package build

import "github.com/bitwise-media-group/patchy/internal/mirror/scan"

// Publish actions, one per (registry, tag) record.
const (
	// ActionPushed means the tag was absent and now points at the build.
	ActionPushed = "pushed"
	// ActionSkippedCurrent means the tag already points at this digest.
	ActionSkippedCurrent = "skipped-current"
	// ActionSkippedTagExists means the tag points at a different digest
	// and was left alone: tags never move.
	ActionSkippedTagExists = "skipped-tag-exists"
	// ActionWouldPush is the dry-run row: no registry was contacted.
	ActionWouldPush = "would-push"
)

// Record is one published tag's outcome at one registry.
type Record struct {
	// Registry is the configured registry name.
	Registry string `json:"registry"`
	// Ref is <registry.url>/<repository>:<tag>.
	Ref string `json:"ref"`
	// Digest is what the tag points at after the run (for
	// skipped-tag-exists, the other digest it already held).
	Digest string `json:"digest,omitempty"`
	// Action is pushed, skipped-current, skipped-tag-exists or would-push.
	Action string `json:"action"`
	// Signed reports a signature created this run for the digest.
	Signed bool `json:"signed"`
}

// ScanReport is one entry's pre-push scan outcome.
type ScanReport struct {
	// Skipped names why the vulnerability scan did not run, if it did not.
	Skipped string `json:"skipped,omitempty"`
	// Scanned lists the platforms scanned (images).
	Scanned []string `json:"scanned,omitempty"`
	// Blocking are blocking-severity findings the allowlist did not
	// suppress; any entry here fails the build before anything is pushed.
	Blocking []scan.Finding `json:"blocking,omitempty"`
	// Suppressed counts findings the allowlist accepted.
	Suppressed int `json:"suppressed"`
	// Expired lists allowlist IDs whose expiry has lapsed; they no longer
	// suppress anything.
	Expired []string `json:"expired,omitempty"`
	// ConfigScan reports the kubescape sweep (manifests): "clean",
	// "failed", or "skipped: <reason>".
	ConfigScan string `json:"configScan,omitempty"`
	// ConfigScanFailed blocks under kubescape fail mode.
	ConfigScanFailed bool `json:"configScanFailed,omitempty"`
}

// Failed reports whether the scan gate blocks the publish.
func (r *ScanReport) Failed() bool { return r != nil && (len(r.Blocking) > 0 || r.ConfigScanFailed) }

// Result is one entry's build outcome.
type Result struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Digest is the built manifest (or index) digest.
	Digest string `json:"digest,omitempty"`
	// Tags are the expanded tags.
	Tags    []string    `json:"tags,omitempty"`
	Scan    *ScanReport `json:"scan,omitempty"`
	Records []Record    `json:"records,omitempty"`
	// Err carries a per-entry failure in --all summaries.
	Err string `json:"error,omitempty"`
}

// AllowlistResult reports one --update-allowlists regeneration.
type AllowlistResult struct {
	Name string `json:"name"`
	// Path is the allowlist file, relative to the working directory.
	Path string `json:"path,omitempty"`
	// Generated is false when the entry was skipped (Note says why).
	Generated bool     `json:"generated"`
	Kept      int      `json:"kept"`
	Renewed   []string `json:"renewed,omitempty"`
	Added     int      `json:"added"`
	Dropped   []string `json:"dropped,omitempty"`
	// Note explains a skip.
	Note string `json:"note,omitempty"`
	// Err carries a per-entry failure in --all summaries.
	Err string `json:"error,omitempty"`
}

// Event narrates engine progress for stderr; results are the data.
type Event struct {
	// Kind is "note" or "warn".
	Kind  string `json:"kind"`
	Entry string `json:"entry,omitempty"`
	Stage string `json:"stage,omitempty"`
	// Message is the human-readable line.
	Message string `json:"message"`
}
