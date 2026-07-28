// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package wiz

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bitwise-media-group/patchy/pkg/source"
)

// Source identifiers stamped into the security-source label — one per feed,
// so provenance and write-back dispatch can tell an issue from a detection.
const (
	// IssuesID identifies the Wiz Issues feed.
	IssuesID = "wiz-issues"
	// DefendID identifies the Wiz Defend threat-detection feed.
	DefendID = "wiz-defend"
)

// Event discriminators. Both feeds share one path and carry no event header,
// so Detect derives these from the payload shape.
const (
	// EventIssue is a Wiz Issues delivery.
	EventIssue = "wiz.issue"
	// EventThreat is a Wiz Defend delivery.
	EventThreat = "wiz.threat"
)

// Credential Secret keys.
const (
	// KeyWebhookToken is the shared bearer token inbound deliveries carry.
	KeyWebhookToken = "webhookToken"
	// KeyClientID and KeyClientSecret are the Wiz API service-account
	// credentials, present only when write-back is configured.
	KeyClientID     = "clientId"
	KeyClientSecret = "clientSecret"
)

// severityRank orders the Wiz severity vocabulary so a minimum can be
// applied. INFORMATIONAL sorts below LOW: the default floor drops it.
var severityRank = map[string]int{
	"INFORMATIONAL": 0,
	"LOW":           1,
	"MEDIUM":        2,
	"HIGH":          3,
	"CRITICAL":      4,
}

// patchySeverity maps the Wiz vocabulary onto patchy's label scale. An
// informational finding gets no severity rather than a made-up one.
var patchySeverity = map[string]string{
	"LOW":      "low",
	"MEDIUM":   "medium",
	"HIGH":     "high",
	"CRITICAL": "critical",
}

// cloudPlatforms maps Wiz's platform vocabulary onto patchy's CloudProvider
// values. Platforms outside the map (Kubernetes, OCI, Alibaba, ...) are not
// supported in v1: their findings are skipped, because a Finding's cloud
// resource must carry a provider the rest of the pipeline understands.
var cloudPlatforms = map[string]string{
	"GCP":   "google",
	"AWS":   "aws",
	"AZURE": "azure",
}

// actionableTriggers are the lifecycle events that create or refresh a
// Finding. Resolved is deliberately absent: propagating an external resolve
// into the state machine is future work, documented as such.
var actionableTriggers = map[string]bool{
	"CREATED":  true,
	"UPDATED":  true,
	"REOPENED": true,
}

// Options configure a handler.
type Options struct {
	// MinSeverity drops findings below this level, using patchy's vocabulary
	// (low/medium/high/critical). Empty admits everything above
	// informational; informational is always dropped when a floor is set.
	MinSeverity string
}

// Detect reports which feed a body belongs to by its shape: EventIssue for a
// top-level issue object, EventThreat for a top-level threat object, "ping"
// for valid JSON that is neither (Wiz's test delivery). Undecodable bodies
// are an error — the caller answers 400, not 204.
func Detect(body []byte) (string, error) {
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return "", fmt.Errorf("wiz: decode delivery: %w", err)
	}
	switch {
	case env.Issue != nil:
		return EventIssue, nil
	case env.Threat != nil:
		return EventThreat, nil
	default:
		return "ping", nil
	}
}

// DeliveryID derives a dedup key for a delivery. Wiz sends no delivery GUID,
// so the honest key is the body itself: byte-identical redeliveries dedup,
// distinct events (which always differ in ids or timestamps) never do.
func DeliveryID(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])[:16]
}

// IssuesHandler is the Wiz Issues source plugin.
type IssuesHandler struct {
	opts Options
}

var _ source.Handler = (*IssuesHandler)(nil)

// NewIssues builds the Issues handler. It takes no client: the documented
// webhook template carries everything patchy needs.
func NewIssues(o Options) *IssuesHandler { return &IssuesHandler{opts: o} }

// ID implements source.Handler.
func (h *IssuesHandler) ID() string { return IssuesID }

// Findings implements source.Handler: it normalizes one issue delivery into
// at most one Finding. Deliveries the pipeline does not act on return
// (nil, nil).
func (h *IssuesHandler) Findings(_ context.Context, event string, payload []byte) ([]source.Finding, error) {
	if event != EventIssue {
		return nil, nil
	}
	var env envelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return nil, fmt.Errorf("wiz: decode issue delivery: %w", err)
	}
	iss := env.Issue
	if iss == nil || iss.ID == "" {
		return nil, fmt.Errorf("wiz: delivery missing issue.id")
	}
	if !actionable(env.Trigger.Type, iss.Status, iss.Severity, h.opts.MinSeverity) {
		return nil, nil
	}
	cr := issueResource(env.EntitySnapshot)
	if cr == nil {
		return nil, nil // unsupported platform or no entity: nothing to accumulate against
	}
	description, err := renderIssueDescription(&env)
	if err != nil {
		return nil, err
	}
	return []source.Finding{{
		Source:        IssuesID,
		AlertID:       iss.ID,
		CloudResource: cr,
		Advisories:    issueAdvisories(iss),
		RuleID:        iss.Control.ID,
		Title:         issueTitle(&env),
		Description:   description,
		Severity:      patchySeverity[strings.ToUpper(iss.Severity)],
		HTMLURL:       iss.URL,
	}}, nil
}

// DefendHandler is the Wiz Defend source plugin.
type DefendHandler struct {
	opts Options
}

var _ source.Handler = (*DefendHandler)(nil)

// NewDefend builds the Defend handler.
func NewDefend(o Options) *DefendHandler { return &DefendHandler{opts: o} }

// ID implements source.Handler.
func (h *DefendHandler) ID() string { return DefendID }

// Findings implements source.Handler: one Finding per supported affected
// resource, so each accumulates against the resource it concerns. A threat
// with no resources falls back to one Finding per cloud account — coarse,
// but a detection with no subject at all is undeliverable and errors.
func (h *DefendHandler) Findings(_ context.Context, event string, payload []byte) ([]source.Finding, error) {
	if event != EventThreat {
		return nil, nil
	}
	var env envelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return nil, fmt.Errorf("wiz: decode threat delivery: %w", err)
	}
	th := env.Threat
	if th == nil || th.ID == "" {
		return nil, fmt.Errorf("wiz: delivery missing threat.id")
	}
	if !actionable(env.Trigger.Type, th.Status, th.Severity, h.opts.MinSeverity) {
		return nil, nil
	}
	description, err := renderThreatDescription(&env)
	if err != nil {
		return nil, err
	}
	base := source.Finding{
		Source:      DefendID,
		AlertID:     th.ID,
		Advisories:  threatAdvisories(th),
		RuleID:      th.RuleID,
		Title:       threatTitle(th),
		Description: description,
		Severity:    patchySeverity[strings.ToUpper(th.Severity)],
		HTMLURL:     th.URL,
	}
	var out []source.Finding
	for i := range th.Resources {
		if cr := threatResourceOf(th, &th.Resources[i]); cr != nil {
			f := base
			f.CloudResource = cr
			out = append(out, f)
		}
	}
	if len(out) == 0 {
		for _, account := range th.CloudAccounts {
			if cr := accountResource(th, account); cr != nil {
				f := base
				f.CloudResource = cr
				out = append(out, f)
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("wiz: threat %s names no supported resource or cloud account", th.ID)
	}
	return out, nil
}

// actionable reports whether a delivery should become a Finding: an
// actionable lifecycle event, a status that is still open, and a severity
// clearing the floor.
func actionable(triggerType, status, severity, minSeverity string) bool {
	if !actionableTriggers[strings.ToUpper(triggerType)] {
		return false
	}
	switch strings.ToUpper(status) {
	case "", "OPEN", "IN_PROGRESS":
	default:
		return false
	}
	return meetsMinSeverity(severity, minSeverity)
}

// meetsMinSeverity applies the configured floor.
func meetsMinSeverity(severity, minSeverity string) bool {
	if minSeverity == "" {
		return true
	}
	floor, ok := severityRank[strings.ToUpper(minSeverity)]
	if !ok {
		return true // unrecognized configuration must not silently drop findings
	}
	return severityRank[strings.ToUpper(severity)] >= floor
}

// issueAdvisories keys accumulation on the security control — the issue's
// type, so re-notifications of the same misconfiguration on the same
// resource fold, exactly as SCC's category does. The CR requires at least
// one advisory, so this never returns empty.
func issueAdvisories(iss *issue) []string {
	if iss.Control.ID != "" {
		return []string{"wiz-control:" + iss.Control.ID}
	}
	// An issue without a control is malformed, but dropping it silently
	// would be worse than tracking it against its own id.
	return []string{"wiz-issue:" + iss.ID}
}

// threatAdvisories keys accumulation on the detection rule — the threat's
// type — with the MITRE techniques appended as secondary labels.
func threatAdvisories(th *threat) []string {
	var out []string
	switch {
	case th.RuleID != "":
		out = append(out, "wiz-rule:"+th.RuleID)
	case len(th.DetectionIDs) > 0:
		out = append(out, "wiz-detection:"+th.DetectionIDs[0])
	default:
		out = append(out, "wiz-threat:"+th.ID)
	}
	for _, t := range th.MitreTechniques {
		if t != "" {
			out = append(out, strings.ToUpper(t))
		}
	}
	return out
}

// issueTitle is the human summary line: the control's name, else the
// automation rule's.
func issueTitle(env *envelope) string {
	if env.Issue.Control.Name != "" {
		return env.Issue.Control.Name
	}
	if env.Trigger.RuleName != "" {
		return env.Trigger.RuleName
	}
	return "Wiz issue " + env.Issue.ID
}

// threatTitle is the human summary line for a detection.
func threatTitle(th *threat) string {
	if th.Name != "" {
		return th.Name
	}
	if th.RuleName != "" {
		return th.RuleName
	}
	return "Wiz threat " + th.ID
}

// issueResource maps an entity snapshot onto the seam type, or nil when the
// platform is unsupported or there is no entity at all.
func issueResource(es *entitySnapshot) *source.CloudResource {
	if es == nil {
		return nil
	}
	return cloudResource(es.CloudPlatform, es.ProviderID, resourceType(es.NativeType, es.Type),
		es.SubscriptionExternalID, es.Region, es.Name)
}

// threatResourceOf maps one affected resource onto the seam type, falling
// back to the threat's own platform when the resource carries none.
func threatResourceOf(th *threat, res *threatResource) *source.CloudResource {
	platform := res.CloudPlatform
	if platform == "" {
		platform = th.CloudPlatform
	}
	return cloudResource(platform, res.ProviderID, resourceType(res.NativeType, res.Type),
		res.SubscriptionExternalID, res.Region, res.Name)
}

// accountResource is the per-cloud-account fallback scope for a threat that
// names no individual resource.
func accountResource(th *threat, account string) *source.CloudResource {
	if account == "" {
		return nil
	}
	return cloudResource(th.CloudPlatform, "wiz-account:"+account, "", account, "", account)
}

// cloudResource builds the seam type, normalizing Google Cloud identifiers
// to the Cloud Asset Inventory name form. Nil when the platform is
// unsupported or the resource has no identifier.
func cloudResource(platform, providerID, rtype, project, location, displayName string) *source.CloudResource {
	provider := cloudPlatforms[strings.ToUpper(platform)]
	if provider == "" || providerID == "" {
		return nil
	}
	name := providerID
	if provider == "google" {
		name = NormalizeGCPName(name)
	}
	return &source.CloudResource{
		Provider:    provider,
		Name:        name,
		Type:        rtype,
		Project:     project,
		Location:    location,
		DisplayName: displayName,
	}
}

// resourceType prefers the provider's own type name over Wiz's abstraction.
func resourceType(nativeType, wizType string) string {
	if nativeType != "" {
		return nativeType
	}
	return wizType
}
