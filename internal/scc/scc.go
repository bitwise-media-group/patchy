// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package scc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bitwise-media-group/patchy/pkg/source"
)

// ID is the source identifier stamped into the security-source label.
const ID = "gcp-scc"

// EventType is the event discriminator for this source. SCC's endpoint
// carries exactly one kind of delivery, so unlike a GitHub webhook there is no
// header to read it from — the route itself is the discriminator.
const EventType = "scc.notification"

// CloudProvider is the platform value stamped on every finding's cloud
// resource.
const CloudProvider = "google"

// severityRank orders the SCC severity vocabulary so a minimum can be applied.
// SEVERITY_UNSPECIFIED sorts below LOW: a finding whose detector declined to
// rate it should not clear a threshold by accident.
var severityRank = map[string]int{
	"SEVERITY_UNSPECIFIED": 0,
	"LOW":                  1,
	"MEDIUM":               2,
	"HIGH":                 3,
	"CRITICAL":             4,
}

// patchySeverity maps the SCC vocabulary onto patchy's label scale. An
// unrated finding gets no severity rather than a made-up one.
var patchySeverity = map[string]string{
	"LOW":      "low",
	"MEDIUM":   "medium",
	"HIGH":     "high",
	"CRITICAL": "critical",
}

// skippedClasses are finding classes that are not security findings about a
// resource. SCC_ERROR reports that a detector could not run — an operational
// problem for whoever owns the SCC configuration, not something to triage or
// remediate.
var skippedClasses = map[string]bool{"SCC_ERROR": true}

// Options configure the handler.
type Options struct {
	// MinSeverity drops findings below this level, using patchy's vocabulary
	// (low/medium/high/critical). Empty admits everything.
	MinSeverity string
	// Organization is the numeric Google Cloud organization id, used to
	// compose a Console link for findings that carry no externalUri.
	Organization string
}

// Handler is the Security Command Center source plugin. It does not implement
// source.Resolver: muting a finding needs a Google Cloud write credential the
// integration-controller does not hold, so the write-back is deliberately
// absent rather than present and failing.
type Handler struct {
	opts Options
}

var _ source.Handler = (*Handler)(nil)

// New builds the handler. It takes no client: an SCC notification carries
// everything patchy needs, so unlike the GHAS source there is nothing to fetch.
func New(o Options) *Handler { return &Handler{opts: o} }

// ID implements source.Handler.
func (h *Handler) ID() string { return ID }

// Findings implements source.Handler: it unwraps the Pub/Sub envelope,
// decodes the notification, and normalizes it into at most one Finding.
// Deliveries the pipeline does not act on return (nil, nil).
func (h *Handler) Findings(_ context.Context, event string, payload []byte) ([]source.Finding, error) {
	if event != EventType {
		return nil, nil
	}
	n, err := Decode(payload)
	if err != nil {
		return nil, err
	}
	f := &n.Finding
	if f.Name == "" {
		return nil, fmt.Errorf("scc: notification missing finding.name")
	}
	if !h.actionable(f) {
		return nil, nil
	}

	description, err := renderDescription(n)
	if err != nil {
		return nil, err
	}

	out := source.Finding{
		Source:        ID,
		AlertID:       f.Name,
		CloudResource: cloudResource(n),
		Advisories:    advisories(f),
		RuleID:        ruleID(f),
		Title:         title(f),
		Description:   description,
		Severity:      patchySeverity[strings.ToUpper(f.Severity)],
		HTMLURL:       h.findingURL(f),
	}
	return []source.Finding{out}, nil
}

// Decode unwraps a Pub/Sub push envelope into the SCC notification it
// carries. Exported so the receiver can read the delivery id off the same
// body it validates, without decoding twice in two places.
func Decode(payload []byte) (*notification, error) {
	var env pushEnvelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return nil, fmt.Errorf("scc: decode pub/sub envelope: %w", err)
	}
	if env.Message.Data == "" {
		return nil, fmt.Errorf("scc: pub/sub envelope carries no message data")
	}
	raw, err := base64.StdEncoding.DecodeString(env.Message.Data)
	if err != nil {
		return nil, fmt.Errorf("scc: decode message data: %w", err)
	}
	var n notification
	if err := json.Unmarshal(raw, &n); err != nil {
		return nil, fmt.Errorf("scc: decode notification: %w", err)
	}
	return &n, nil
}

// DeliveryID reads the Pub/Sub message id from a push envelope, for the
// receiver's dedup window. An undecodable body yields "", which the receiver
// treats as "cannot dedup" rather than an error — the body is validated
// downstream.
func DeliveryID(payload []byte) string {
	var env pushEnvelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return ""
	}
	return env.id()
}

// actionable reports whether a finding should become a patchy Finding.
func (h *Handler) actionable(f *finding) bool {
	if !strings.EqualFold(f.State, "ACTIVE") {
		return false
	}
	if strings.EqualFold(f.Mute, "MUTED") {
		return false
	}
	if skippedClasses[strings.ToUpper(f.FindingClass)] {
		return false
	}
	return h.meetsMinSeverity(f.Severity)
}

// meetsMinSeverity applies the configured floor.
func (h *Handler) meetsMinSeverity(severity string) bool {
	if h.opts.MinSeverity == "" {
		return true
	}
	floor, ok := severityRank[strings.ToUpper(h.opts.MinSeverity)]
	if !ok {
		return true // unrecognized configuration must not silently drop findings
	}
	return severityRank[strings.ToUpper(severity)] >= floor
}

// advisories extracts the categorization identifiers, most authoritative
// first. SCC findings mostly carry no CVE — a misconfiguration is not a
// vulnerability — so the category fallback is the common case, not the
// exception, and it is what keys accumulation. The CR requires at least one,
// so this never returns empty.
func advisories(f *finding) []string {
	var out []string
	if f.Vulnerability != nil && f.Vulnerability.CVE != nil && f.Vulnerability.CVE.ID != "" {
		out = append(out, strings.ToUpper(f.Vulnerability.CVE.ID))
	}
	if f.Category != "" {
		out = append(out, "category:"+f.Category)
	}
	if len(out) == 0 {
		// A finding with neither is malformed, but dropping it silently would
		// be worse than tracking it against its own name.
		out = append(out, "scc:"+lastSegment(f.Name))
	}
	return out
}

// ruleID names what fired: the detector module when SCC reports one, else the
// category.
func ruleID(f *finding) string {
	if f.ModuleName != "" {
		return f.ModuleName
	}
	return f.Category
}

// title is the human summary line. The category is SCC's compact identifier
// and reads well once un-shouted (PUBLIC_BUCKET_ACL → "Public bucket ACL");
// the full prose lives in the description.
func title(f *finding) string {
	if f.Category == "" {
		return lastSegment(f.Name)
	}
	return humanize(f.Category)
}

// humanize turns SCREAMING_SNAKE_CASE into a sentence-cased phrase.
func humanize(category string) string {
	words := strings.Split(strings.ToLower(category), "_")
	for i, w := range words {
		if w == "" {
			continue
		}
		if i == 0 {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}

// cloudResource maps the notification's resource block onto the seam type.
// finding.resourceName is authoritative for the identifier — the resource
// block is a convenience projection and can be sparse.
func cloudResource(n *notification) *source.CloudResource {
	name := n.Finding.ResourceName
	if name == "" {
		name = n.Resource.Name
	}
	if name == "" {
		return nil
	}
	return &source.CloudResource{
		Provider:    CloudProvider,
		Name:        name,
		Type:        n.Resource.Type,
		Project:     n.Resource.Project,
		Location:    n.Resource.Location,
		DisplayName: n.Resource.DisplayName,
	}
}

// findingURL links back to the finding. SCC sets externalUri on most
// findings; where it does not, a Console link composed from the organization
// is better than nothing.
func (h *Handler) findingURL(f *finding) string {
	if f.ExternalURI != "" {
		return f.ExternalURI
	}
	if h.opts.Organization == "" {
		return ""
	}
	return fmt.Sprintf(
		"https://console.cloud.google.com/security/command-center/findings?organizationId=%s",
		h.opts.Organization)
}

// lastSegment returns the text after the final "/", for naming a finding by
// its id rather than its full resource path.
func lastSegment(name string) string {
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[i+1:]
	}
	return name
}
