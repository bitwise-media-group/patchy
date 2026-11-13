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
	record := func(o provenance) {
		if o.gap != "" {
			report.Gaps = append(report.Gaps, o.gap)
		} else {
			report.Verified = append(report.Verified, o.verified)
		}
	}
	o, err := e.verifyOwnSubject(ctx, entry, false)
	if err != nil {
		return nil, err
	}
	record(o)
	if entry.Kind == spec.KindChart {
		lock, err := spec.LoadImagesLock(entry.LockPath())
		if err != nil {
			return nil, fmt.Errorf("%s: no lock (run upgrade first): %w", entry.Name, err)
		}
		for _, img := range lock.Images {
			o, err := e.verifyImageSubject(ctx, entry, img, false)
			if err != nil {
				return nil, err
			}
			record(o)
		}
	}
	for _, gap := range report.Gaps {
		e.warnf(entry.Name, "verify", "upstream provenance gap (documented, not a failure): %s", gap)
	}
	return report, nil
}

// provenance is one subject's outcome: verified names it, or gap explains
// the documented provider-none hole. Exactly one is set.
type provenance struct {
	verified, gap string
}

// verifyOwnSubject verifies the entry's own upstream artifact (the chart
// or the mirrored OCI artifact) per its verifyUpstream rule. planOnly
// resolves the rule and narrates what would be verified without calling
// cosign (sync --dry-run).
func (e *Engine) verifyOwnSubject(ctx context.Context, entry spec.Entry, planOnly bool) (provenance, error) {
	var rule spec.VerifyRule
	var ref, subject string
	if entry.Kind == spec.KindChart {
		m := entry.Chart
		rule = m.Chart.VerifyUpstream
		subject = "chart " + m.Chart.Name
		if rule.EffectiveProvider() == "none" {
			return provenance{gap: subject + " (no upstream provenance declared)"}, nil
		}
		if !strings.HasPrefix(m.Chart.Repo, "oci://") {
			return provenance{}, fmt.Errorf(
				"%s: chart verifyUpstream is only supported for oci:// repos (got %s); use provider: none",
				entry.Name, m.Chart.Repo)
		}
		ref = e.rewrite(strings.TrimPrefix(m.Chart.Repo, "oci://")+"/"+m.Chart.Name) + ":" + m.Chart.Version
	} else {
		a := entry.Artifact.Artifact
		rule = a.VerifyUpstream
		subject = "artifact " + a.Ref
		if rule.EffectiveProvider() == "none" {
			return provenance{gap: subject + " (no upstream provenance declared)"}, nil
		}
		ref = e.rewrite(a.Ref) + ":" + a.Version
	}
	if err := e.verifyRef(ctx, entry.Name, subject, ref, rule, planOnly); err != nil {
		return provenance{}, err
	}
	return provenance{verified: subject}, nil
}

// verifyImageSubject verifies one locked image per the first matching
// images.verifyUpstream rule; an image no rule covers is an error.
// planOnly is as for verifyOwnSubject.
func (e *Engine) verifyImageSubject(ctx context.Context, entry spec.Entry, img spec.LockImage,
	planOnly bool) (provenance, error) {
	rule, matched := matchVerifyRule(entry.Chart.Images.VerifyUpstream, img.Source)
	if !matched {
		return provenance{}, fmt.Errorf(
			"%s: no verifyUpstream rule matches image %s; add a rule (provider: none to document a gap)",
			entry.Name, img.Source)
	}
	if rule.EffectiveProvider() == "none" {
		return provenance{gap: fmt.Sprintf("%s (rule: %s)", img.Source, rule.Match)}, nil
	}
	srcRef, err := imageref.Parse(img.Source)
	if err != nil {
		return provenance{}, fmt.Errorf("%s: %w", entry.Name, err)
	}
	ref := e.rewrite(srcRef.Repository) + "@" + img.Digest
	if err := e.verifyRef(ctx, entry.Name, "image "+img.Source, ref, rule.VerifyRule, planOnly); err != nil {
		return provenance{}, err
	}
	return provenance{verified: img.Source}, nil
}

// verifyRef checks one upstream signature with cosign (or, planOnly, only
// narrates that it would).
func (e *Engine) verifyRef(ctx context.Context, entryName, subject, ref string, rule spec.VerifyRule,
	planOnly bool) error {
	s, err := verify.UpstreamSubject(ref, rule)
	if err != nil {
		return fmt.Errorf("%s: %s: %w", entryName, subject, err)
	}
	if planOnly {
		e.notef(entryName, "verify", "would verify %s (%s)", ref, rule.EffectiveProvider())
		return nil
	}
	e.notef(entryName, "verify", "verifying %s (%s)", ref, rule.EffectiveProvider())
	if err := e.verifyFn(ctx, s); err != nil {
		return fmt.Errorf("%s: upstream signature verification failed for %s: %w", entryName, subject, err)
	}
	return nil
}

// upstreamGate verifies upstream provenance lazily for one Sync call: a
// subject is verified the first time sync is about to push or sign it,
// at most once however many registries it publishes to, and never when
// every copy is already current and signed. Documented gaps narrate once.
type upstreamGate struct {
	e        *Engine
	entry    spec.Entry
	planOnly bool
	done     map[string]error
}

// newUpstreamGate builds the gate for one entry's sync.
func (e *Engine) newUpstreamGate(entry spec.Entry, dryRun bool) *upstreamGate {
	return &upstreamGate{e: e, entry: entry, planOnly: dryRun, done: map[string]error{}}
}

// own gates the entry's chart or artifact.
func (g *upstreamGate) own(ctx context.Context) error {
	return g.once("\x00own", func() (provenance, error) {
		return g.e.verifyOwnSubject(ctx, g.entry, g.planOnly)
	})
}

// image gates one locked image.
func (g *upstreamGate) image(ctx context.Context, img spec.LockImage) error {
	return g.once(img.Source+"@"+img.Digest, func() (provenance, error) {
		return g.e.verifyImageSubject(ctx, g.entry, img, g.planOnly)
	})
}

// once runs a subject's check the first time and replays its verdict.
func (g *upstreamGate) once(key string, check func() (provenance, error)) error {
	if err, ok := g.done[key]; ok {
		return err
	}
	o, err := check()
	if err == nil && o.gap != "" {
		g.e.warnf(g.entry.Name, "verify", "upstream provenance gap (documented, not a failure): %s", o.gap)
	}
	g.done[key] = err
	return err
}

// matchVerifyRule finds the first rule whose path glob matches source.
func matchVerifyRule(rules []spec.ImageVerifyRule, source string) (spec.ImageVerifyRule, bool) {
	for _, r := range rules {
		if r.Match != "" && imageref.PathMatch(r.Match, source) {
			return r, true
		}
	}
	return spec.ImageVerifyRule{}, false
}
