// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package mirror

import (
	"context"
	"fmt"
	"strings"

	"github.com/bitwise-media-group/patchy/internal/mirror/imageref"
	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
	"github.com/bitwise-media-group/patchy/internal/mirror/verify"
)

// VerifyReport is one entry's upstream-provenance outcome. Gaps are
// subjects a provider-none rule deliberately leaves unverified — reported,
// never silently dropped, and never failures.
type VerifyReport struct {
	Verified []string `json:"verified,omitempty"`
	Gaps     []string `json:"gaps,omitempty"`
}

// VerifyUpstream checks the entry's upstream provenance per its manifest
// rules: the chart or artifact itself, then every locked image (first
// matching rule wins; every image must match one).
func (e *Engine) VerifyUpstream(ctx context.Context, entry spec.Entry) (*VerifyReport, error) {
	report := &VerifyReport{}
	if entry.Kind == spec.KindArtifact {
		if err := e.verifySubjectRule(ctx, entry, report); err != nil {
			return nil, err
		}
		return report, nil
	}
	if err := e.verifySubjectRule(ctx, entry, report); err != nil {
		return nil, err
	}
	if err := e.verifyLockedImages(ctx, entry, report); err != nil {
		return nil, err
	}
	for _, gap := range report.Gaps {
		e.warnf(entry.Name, "verify", "upstream provenance gap (documented, not a failure): %s", gap)
	}
	return report, nil
}

// verifySubjectRule verifies the entry's own artifact (chart tgz artifact
// or mirrored OCI artifact).
func (e *Engine) verifySubjectRule(ctx context.Context, entry spec.Entry, report *VerifyReport) error {
	var rule spec.VerifyRule
	var ref, subject string
	if entry.Kind == spec.KindChart {
		m := entry.Chart
		rule = m.Chart.VerifyUpstream
		subject = "chart " + m.Chart.Name
		if rule.EffectiveProvider() == "none" {
			report.Gaps = append(report.Gaps, subject+" (no upstream provenance declared)")
			return nil
		}
		if !strings.HasPrefix(m.Chart.Repo, "oci://") {
			return fmt.Errorf("%s: chart verifyUpstream is only supported for oci:// repos (got %s); use provider: none",
				entry.Name, m.Chart.Repo)
		}
		ref = e.rewrite(strings.TrimPrefix(m.Chart.Repo, "oci://")+"/"+m.Chart.Name) + ":" + m.Chart.Version
	} else {
		a := entry.Artifact.Artifact
		rule = a.VerifyUpstream
		subject = "artifact " + a.Ref
		if rule.EffectiveProvider() == "none" {
			report.Gaps = append(report.Gaps, subject+" (no upstream provenance declared)")
			return nil
		}
		ref = e.rewrite(a.Ref) + ":" + a.Version
	}
	e.notef(entry.Name, "verify", "verifying %s (%s)", ref, rule.EffectiveProvider())
	s, err := verify.UpstreamSubject(ref, rule)
	if err != nil {
		return fmt.Errorf("%s: %s: %w", entry.Name, subject, err)
	}
	if err := e.verifyFn(ctx, s); err != nil {
		return fmt.Errorf("%s: signature verification failed for %s: %w", entry.Name, subject, err)
	}
	report.Verified = append(report.Verified, subject)
	return nil
}

// verifyLockedImages applies the images.verifyUpstream rules to every
// locked image.
func (e *Engine) verifyLockedImages(ctx context.Context, entry spec.Entry, report *VerifyReport) error {
	lock, err := spec.LoadImagesLock(entry.LockPath())
	if err != nil {
		return fmt.Errorf("%s: no lock (run upgrade first): %w", entry.Name, err)
	}
	rules := entry.Chart.Images.VerifyUpstream
	for _, img := range lock.Images {
		rule, matched := matchVerifyRule(rules, img.Source)
		if !matched {
			return fmt.Errorf("%s: no verifyUpstream rule matches image %s; add a rule (provider: none to document a gap)",
				entry.Name, img.Source)
		}
		if rule.EffectiveProvider() == "none" {
			report.Gaps = append(report.Gaps, fmt.Sprintf("%s (rule: %s)", img.Source, rule.Match))
			continue
		}
		srcRef, err := imageref.Parse(img.Source)
		if err != nil {
			return fmt.Errorf("%s: %w", entry.Name, err)
		}
		ref := e.rewrite(srcRef.Repository) + "@" + img.Digest
		e.notef(entry.Name, "verify", "verifying %s (%s)", img.Source, rule.EffectiveProvider())
		s, err := verify.UpstreamSubject(ref, rule.VerifyRule)
		if err != nil {
			return fmt.Errorf("%s: rule %s: %w", entry.Name, rule.Match, err)
		}
		if err := e.verifyFn(ctx, s); err != nil {
			return fmt.Errorf("%s: image signature verification failed for %s: %w", entry.Name, img.Source, err)
		}
		report.Verified = append(report.Verified, img.Source)
	}
	return nil
}

// matchVerifyRule finds the first rule whose glob matches source.
func matchVerifyRule(rules []spec.ImageVerifyRule, source string) (spec.ImageVerifyRule, bool) {
	for _, r := range rules {
		if r.Match != "" && imageref.GlobMatch(r.Match, source) {
			return r, true
		}
	}
	return spec.ImageVerifyRule{}, false
}
