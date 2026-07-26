// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package scc

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/bitwise-media-group/patchy/internal/templates"
)

// maxProperties caps how many detector properties are rendered. Some
// detectors emit a large bag; the description is read by a human and an agent,
// and neither is served by an unbounded table.
const maxProperties = 24

// renderDescription turns the notification into the Finding's markdown
// description. Mapping rather than dumping the payload is deliberate: the
// description is a document two audiences read, so the shape is the source's
// decision even though the prose is the detector's.
func renderDescription(n *notification) (string, error) {
	f := &n.Finding
	view := templates.SCCFinding{
		Name:        f.Name,
		Category:    f.Category,
		Class:       f.FindingClass,
		Severity:    strings.ToLower(f.Severity),
		Description: strings.TrimSpace(f.Description),
		NextSteps:   strings.TrimSpace(f.NextSteps),
		Resource: templates.SCCResource{
			Name:        resourceName(n),
			DisplayName: n.Resource.DisplayName,
			Type:        n.Resource.Type,
			Project:     n.Resource.Project,
			Location:    n.Resource.Location,
			Service:     n.Resource.Service,
		},
		DetectedAt:  f.EventTime,
		ExternalURI: f.ExternalURI,
		Properties:  properties(f.SourceProperties),
		Marks:       marks(f.SecurityMarks),
	}
	if f.Severity == "" || strings.EqualFold(f.Severity, "SEVERITY_UNSPECIFIED") {
		view.Severity = ""
	}
	if v := f.Vulnerability; v != nil && v.CVE != nil {
		view.CVE = strings.ToUpper(v.CVE.ID)
		if v.CVE.Cvssv3 != nil && v.CVE.Cvssv3.BaseScore > 0 {
			view.CVSS = strconv.FormatFloat(v.CVE.Cvssv3.BaseScore, 'f', -1, 64)
		}
	}
	if m := f.MitreAttack; m != nil {
		view.MitreTactic = m.PrimaryTactic
		view.MitreTechniques = m.PrimaryTechniques
	}
	for _, c := range f.Compliances {
		if len(c.IDs) == 0 {
			continue
		}
		view.Compliances = append(view.Compliances, templates.SCCCompliance{
			Standard: c.Standard, Version: c.Version, IDs: c.IDs,
		})
	}
	return templates.RenderSCCDescription(view)
}

// resourceName prefers the finding's own resourceName over the resource
// block's, matching cloudResource.
func resourceName(n *notification) string {
	if n.Finding.ResourceName != "" {
		return n.Finding.ResourceName
	}
	return n.Resource.Name
}

// properties flattens sourceProperties into sorted key/value pairs. The values
// are arbitrary JSON, so they are formatted rather than typed; nested objects
// render as their Go form, which is ugly but honest and rare.
func properties(src map[string]any) []templates.SCCKV {
	if len(src) == 0 {
		return nil
	}
	out := make([]templates.SCCKV, 0, len(src))
	for _, k := range slices.Sorted(maps.Keys(src)) {
		v := formatValue(src[k])
		if v == "" {
			continue
		}
		out = append(out, templates.SCCKV{Key: k, Value: v})
		if len(out) == maxProperties {
			break
		}
	}
	return out
}

// marks flattens the operator's security marks into sorted pairs.
func marks(sm *securityMarks) []templates.SCCKV {
	if sm == nil || len(sm.Marks) == 0 {
		return nil
	}
	out := make([]templates.SCCKV, 0, len(sm.Marks))
	for _, k := range slices.Sorted(maps.Keys(sm.Marks)) {
		out = append(out, templates.SCCKV{Key: k, Value: sm.Marks[k]})
	}
	return out
}

// formatValue renders one JSON value for a markdown table cell, collapsing
// newlines and pipes so a multi-line or pipe-bearing value cannot break the
// table it sits in.
func formatValue(v any) string {
	var s string
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		s = t
	case float64:
		s = strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		s = strconv.FormatBool(t)
	default:
		s = fmt.Sprintf("%v", t)
	}
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "|", `\|`)
	return strings.TrimSpace(s)
}
