// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package generic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	pkggeneric "github.com/bitwise-media-group/patchy/pkg/generic"
	"github.com/bitwise-media-group/patchy/pkg/source"
)

// severityRank orders patchy's severity vocabulary so a minimum can be
// applied. Membership doubles as validation: a severity outside the map is
// rejected, never silently admitted.
var severityRank = map[string]int{
	"low":      0,
	"medium":   1,
	"high":     2,
	"critical": 3,
}

// Options configure a handler.
type Options struct {
	// MinSeverity drops findings below this level (low/medium/high/
	// critical). Empty admits everything.
	MinSeverity string
}

// Source is the generic source plugin for one integration.
type Source struct {
	name string
	opts Options
}

var _ source.Handler = (*Source)(nil)

// NewSource builds the handler for the named integration. The name becomes
// the source id of every finding it emits.
func NewSource(name string, o Options) *Source { return &Source{name: name, opts: o} }

// ID implements source.Handler.
func (s *Source) ID() string { return s.name }

// Findings implements source.Handler: it validates one findings delivery and
// maps it onto the seam type. Validation is all-or-nothing — any invalid
// finding fails the whole delivery (the errors are joined), so the external
// process fixes and re-POSTs rather than half its batch silently landing.
func (s *Source) Findings(_ context.Context, event string, payload []byte) ([]source.Finding, error) {
	if event != EventFindings {
		return nil, nil
	}
	var p pkggeneric.Payload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, fmt.Errorf("generic: decode findings delivery: %w", err)
	}
	if len(p.Findings) == 0 {
		return nil, errors.New("generic: findings delivery carries no findings")
	}
	var out []source.Finding
	var errs []error
	for i := range p.Findings {
		f, err := s.finding(&p.Findings[i])
		if err != nil {
			errs = append(errs, fmt.Errorf("finding %d: %w", i, err))
			continue
		}
		if f != nil {
			out = append(out, *f)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("generic: invalid delivery: %w", err)
	}
	return out, nil
}

// finding validates and maps one wire finding; (nil, nil) means dropped by
// the severity floor.
func (s *Source) finding(w *pkggeneric.Finding) (*source.Finding, error) {
	var errs []error
	if w.Title == "" {
		errs = append(errs, errors.New("title is required"))
	}
	if _, ok := severityRank[w.Severity]; !ok {
		errs = append(errs, fmt.Errorf("severity %q is not low, medium, high, or critical", w.Severity))
	}
	if w.Repo == nil && w.CloudResource == nil {
		errs = append(errs, errors.New("a finding names a repo, a cloudResource, or both"))
	}
	if w.Repo != nil && (w.Repo.Owner == "" || w.Repo.Name == "") {
		errs = append(errs, errors.New("repo requires owner and name"))
	}
	if w.CloudResource != nil && (w.CloudResource.Provider == "" || w.CloudResource.Name == "") {
		errs = append(errs, errors.New("cloudResource requires provider and name"))
	}
	if w.AlertID == "" && w.AlertNumber <= 0 {
		errs = append(errs, errors.New("alertId or a positive alertNumber is required"))
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	if floor, ok := severityRank[s.opts.MinSeverity]; ok && severityRank[w.Severity] < floor {
		return nil, nil
	}
	f := &source.Finding{
		Source:        s.name,
		CloudResource: w.CloudResource,
		AlertNumber:   w.AlertNumber,
		AlertID:       w.AlertID,
		Advisories:    s.advisories(w),
		RuleID:        w.RuleID,
		Title:         w.Title,
		Description:   w.Description,
		Severity:      w.Severity,
		HTMLURL:       w.HTMLURL,
		Locations:     w.Locations,
	}
	if w.Repo != nil {
		f.Repo = source.Repo{Owner: w.Repo.Owner, Name: w.Repo.Name}
	}
	return f, nil
}

// advisories keys accumulation. The delivered list wins; absent one, the
// rule folds findings of the same rule, and a finding without even a rule
// accumulates against its own alert identity — never against nothing.
func (s *Source) advisories(w *pkggeneric.Finding) []string {
	if len(w.Advisories) > 0 {
		return w.Advisories
	}
	if w.RuleID != "" {
		return []string{"generic-rule:" + w.RuleID}
	}
	id := w.AlertID
	if id == "" {
		id = strconv.Itoa(w.AlertNumber)
	}
	return []string{"generic-alert:" + id}
}
