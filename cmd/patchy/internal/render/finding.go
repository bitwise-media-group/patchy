// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package render

import (
	"fmt"
	"slices"
	"strings"
	"time"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/printer"
	"github.com/bitwise-media-group/patchy/internal/action"
)

// FindingDetail renders everything known about one finding. spend is the
// cross-attempt total, which lives on the child runs rather than the finding —
// the caller queries it and passes it in, so this package performs no I/O.
func FindingDetail(d *printer.Doc, f *v1alpha1.Finding, now time.Time, spend string) {
	d.Section(fmt.Sprintf("Finding %s", f.Name)).
		Field("Title", f.Spec.Title).
		Field("Advisories", strings.Join(f.Spec.Advisories, ", ")).
		Field("Rule", f.Spec.RuleID).
		Field("Source", f.Spec.Source).
		Field("Severity", string(f.Spec.Severity)).
		Field("Priority", string(f.Status.Priority)).
		Field("Phase", string(f.Status.Phase)).
		Field("Age", Age(f.CreationTimestamp.Time, now))
	if r := f.Spec.Repository; r != nil {
		d.Field("Repository", r.Name).Field("Branch", r.DefaultBranch)
	}
	if f.Spec.Suspend {
		d.Field("Suspended", "yes")
	}
	d.Field("Failure", f.Status.LastFailureReason)

	d.Section("Actions available").
		Field("Now", strings.Join(action.Available(f, now), ", "))
	if a := f.Spec.Approval; a != nil {
		d.Fieldf("Approved by", "%s at %s", a.By, Timestamp(&a.At, now))
	}
	if r := f.Spec.Retry; r != nil {
		d.Fieldf("Retry requested", "%s at %s", r.By, Timestamp(&r.At, now))
	}
	if e := f.Spec.Expedite; e != nil {
		d.Fieldf("Expedited by", "%s at %s", e.By, Timestamp(&e.At, now))
	}

	d.Section("Timeline").
		Field("First observed", Timestamp(f.Status.FirstObservedAt, now)).
		Field("Accumulates until", Timestamp(f.Status.AccumulateUntil, now)).
		Field("Completed", Timestamp(f.Status.CompletedAt, now))
	for _, pt := range f.Status.PhaseTimes {
		d.Field(string(pt.Phase), Timestamp(&pt.At, now))
	}

	d.Section("Ownership").Field("Owners", strings.Join(f.Status.Owners, ", "))
	for _, e := range f.Status.Enrichments {
		d.Fieldf(e.Enhancer, "%s", attrs(e.Attributes))
	}

	FindingTracking(d, f, now)

	d.Section("Runs").
		Fieldf("Attempts", "%d investigation, %d remediation",
			f.Status.Attempts.Investigation, f.Status.Attempts.Remediation)
	if inv := f.Status.Investigation; inv != nil {
		d.Fieldf("Investigation", "%s (attempt %d) verdict %s confidence %s",
			inv.Name, inv.Attempt, Dash(string(inv.Recommendation)), Dash(inv.Confidence))
	}
	if rem := f.Status.Remediation; rem != nil {
		d.Fieldf("Remediation", "%s (attempt %d) success=%t branch %s",
			rem.Name, rem.Attempt, rem.Success, Dash(rem.Branch))
	}
	if ar := f.Status.ActiveRun; ar != nil {
		d.Fieldf("Running now", "%s %s", ar.Kind, ar.Name)
	}
	d.Field("Spend", spend)

	d.Section("Alerts")
	for _, a := range f.Spec.Alerts {
		d.Fieldf(a.ID, "%s", locations(a))
	}
	if f.Spec.OverflowAlerts > 0 {
		d.Fieldf("Not shown", "%d more alerts past the accumulation cap", f.Spec.OverflowAlerts)
	}
}

// FindingSummary renders the header `review` puts above the agent reports:
// enough to know which finding this is, and where its human artefacts live.
func FindingSummary(d *printer.Doc, f *v1alpha1.Finding) {
	d.Section(fmt.Sprintf("Finding %s", f.Name)).
		Field("Title", f.Spec.Title).
		Field("Advisories", strings.Join(f.Spec.Advisories, ", ")).
		Field("Phase", string(f.Status.Phase)).
		Field("Severity", string(f.Spec.Severity)).
		Field("Priority", string(f.Status.Priority))
	if t := f.Status.Tracking; t != nil {
		d.Field("Issue", t.URL)
	}
	if pr := f.Status.PullRequest; pr != nil {
		d.Field("Pull request", pr.URL)
	}
}

// FindingTracking renders the projected tracking item and the remediation pull
// request.
func FindingTracking(d *printer.Doc, f *v1alpha1.Finding, now time.Time) {
	d.Section("Tracking")
	if t := f.Status.Tracking; t != nil {
		d.Fieldf("Issue", "#%d %s", t.IssueNumber, t.URL).Field("State", t.State)
	}
	if pr := f.Status.PullRequest; pr != nil {
		d.Fieldf("Pull request", "#%d %s", pr.Number, pr.URL).
			Field("State", pr.State).
			Field("Merged", Timestamp(pr.MergedAt, now))
	}
}

// FindingURL is a finding's human destination: its tracking issue.
func FindingURL(f *v1alpha1.Finding) string {
	if t := f.Status.Tracking; t != nil {
		return t.URL
	}
	return ""
}

// locations renders an alert's source locations compactly.
func locations(a v1alpha1.Alert) string {
	if len(a.Locations) == 0 {
		return a.URL
	}
	parts := make([]string, 0, len(a.Locations))
	for _, l := range a.Locations {
		if l.StartLine > 0 {
			parts = append(parts, fmt.Sprintf("%s:%d", l.Path, l.StartLine))
			continue
		}
		parts = append(parts, l.Path)
	}
	return strings.Join(parts, ", ")
}

// attrs renders an enrichment's attribute map deterministically — map order
// would otherwise make two runs of `describe` disagree.
func attrs(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", k, m[k]))
	}
	return strings.Join(parts, " ")
}
