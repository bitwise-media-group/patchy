// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package wiz

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/bitwise-media-group/patchy/internal/templates"
)

// maxTags caps how many resource tags are rendered; the description is read
// by a human and an agent, and neither is served by an unbounded table.
const maxTags = 24

// renderIssueDescription turns an issue delivery into the Finding's markdown
// description. Mapping rather than dumping the payload is deliberate: the
// description is a document two audiences read, so the shape is the source's
// decision even though the prose is Wiz's.
func renderIssueDescription(env *envelope) (string, error) {
	iss := env.Issue
	view := templates.WizIssue{
		ID:             iss.ID,
		ControlName:    issueTitle(env),
		ControlID:      iss.Control.ID,
		Severity:       strings.ToLower(iss.Severity),
		Status:         iss.Status,
		Description:    strings.TrimSpace(description(iss)),
		Recommendation: strings.TrimSpace(iss.ResolutionRecommendation),
		Projects:       iss.Projects,
		CreatedAt:      iss.Created,
		URL:            iss.URL,
	}
	if es := env.EntitySnapshot; es != nil {
		view.Entity = templates.WizEntity{
			Name:             entityName(es),
			DisplayName:      es.Name,
			Type:             resourceType(es.NativeType, es.Type),
			Platform:         es.CloudPlatform,
			Subscription:     es.SubscriptionExternalID,
			SubscriptionName: es.SubscriptionName,
			Region:           es.Region,
			ConsoleURL:       es.CloudProviderURL,
			Tags:             tags(es.Tags),
		}
	}
	return templates.RenderWizIssueDescription(view)
}

// renderThreatDescription turns a threat delivery into the Finding's markdown
// description. The threat-wide view is rendered once and shared by every
// per-resource Finding: each names all involved resources and accounts, since
// a responder needs the whole picture regardless of which resource their
// finding accumulated against.
func renderThreatDescription(env *envelope) (string, error) {
	th := env.Threat
	view := templates.WizThreat{
		ID:              th.ID,
		Name:            threatTitle(th),
		RuleName:        th.RuleName,
		RuleID:          th.RuleID,
		Severity:        strings.ToLower(th.Severity),
		Status:          th.Status,
		Description:     strings.TrimSpace(th.Description),
		MitreTactics:    compact(th.MitreTactics),
		MitreTechniques: compact(th.MitreTechniques),
		Detections:      len(th.DetectionIDs),
		CloudAccounts:   compact(th.CloudAccounts),
		CreatedAt:       th.CreatedAt,
		URL:             th.URL,
	}
	if len(th.Resources) > 0 {
		res := &th.Resources[0]
		view.Entity = templates.WizEntity{
			Name:         resourceEntityName(th, res),
			DisplayName:  res.Name,
			Type:         resourceType(res.NativeType, res.Type),
			Platform:     platformOf(th, res),
			Subscription: res.SubscriptionExternalID,
			Region:       res.Region,
		}
	}
	for _, a := range th.Actors {
		if a.Name == "" && a.ID == "" {
			continue
		}
		name := a.Name
		if name == "" {
			name = a.ID
		}
		if a.Type != "" {
			name = fmt.Sprintf("%s (%s)", name, strings.ToLower(a.Type))
		}
		view.Actors = append(view.Actors, name)
	}
	return templates.RenderWizThreatDescription(view)
}

// entityName is the identifier the description shows for an issue's entity —
// the same normalized name the Finding's cloud resource carries, falling
// back to the raw providerId (or Wiz graph id) for unsupported platforms.
func entityName(es *entitySnapshot) string {
	if cr := issueResource(es); cr != nil {
		return cr.Name
	}
	if es.ProviderID != "" {
		return es.ProviderID
	}
	return es.ID
}

// resourceEntityName is the same rule for a threat's resource.
func resourceEntityName(th *threat, res *threatResource) string {
	if cr := threatResourceOf(th, res); cr != nil {
		return cr.Name
	}
	if res.ProviderID != "" {
		return res.ProviderID
	}
	return res.ID
}

// platformOf resolves a resource's platform with the threat-wide fallback.
func platformOf(th *threat, res *threatResource) string {
	if res.CloudPlatform != "" {
		return res.CloudPlatform
	}
	return th.CloudPlatform
}

// description prefers the issue's own prose over the control's.
func description(iss *issue) string {
	if iss.Description != "" {
		return iss.Description
	}
	return iss.Control.Description
}

// tags flattens resource tags into sorted key/value pairs, escaping what
// would break the markdown table they render into.
func tags(src map[string]string) []templates.WizKV {
	if len(src) == 0 {
		return nil
	}
	out := make([]templates.WizKV, 0, len(src))
	for _, k := range slices.Sorted(maps.Keys(src)) {
		v := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(src[k], "\n", " "), "|", `\|`))
		if v == "" {
			continue
		}
		out = append(out, templates.WizKV{Key: k, Value: v})
		if len(out) == maxTags {
			break
		}
	}
	return out
}

// compact drops empty strings, keeping order.
func compact(in []string) []string {
	var out []string
	for _, s := range in {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
