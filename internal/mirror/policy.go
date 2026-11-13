// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package mirror

import (
	"fmt"
	"strings"

	"github.com/bitwise-media-group/patchy/internal/mirror/helmchart"
	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
)

// PolicyError is a blocking source-policy rejection: the entry names a
// source the config's mirror.policy does not permit. Nothing was pulled
// or pushed for it.
type PolicyError struct {
	Entry      string
	Violations []spec.PolicyViolation
}

// Error lists every blocking violation.
func (p *PolicyError) Error() string {
	lines := make([]string, len(p.Violations))
	for i, v := range p.Violations {
		lines[i] = v.String()
	}
	return fmt.Sprintf("%s: %s", p.Entry, strings.Join(lines, "; "))
}

// policySource is one source an entry pulls from.
type policySource struct {
	kind, ref string
}

// policyViolations evaluates sources against the config's source policy,
// returning every rejection (blocking and warning).
func (e *Engine) policyViolations(sources []policySource) []spec.PolicyViolation {
	var out []spec.PolicyViolation
	for _, s := range sources {
		if v := e.global.Policy.Check(s.kind, s.ref); v != nil {
			out = append(out, *v)
		}
	}
	return out
}

// enforcePolicy gates a pull or push: warn-mode violations narrate (once
// per entry and source across stages), and any blocking one fails with a
// *PolicyError before the caller touches a registry.
func (e *Engine) enforcePolicy(entry, stage string, sources ...policySource) error {
	var blocking []spec.PolicyViolation
	for _, v := range e.policyViolations(sources) {
		if v.Blocking() {
			blocking = append(blocking, v)
			continue
		}
		e.warnPolicy(entry, stage, v)
	}
	if len(blocking) > 0 {
		return &PolicyError{Entry: entry, Violations: blocking}
	}
	return nil
}

// warnPolicy emits a warn-mode violation once per entry and source.
func (e *Engine) warnPolicy(entry, stage string, v spec.PolicyViolation) {
	if _, seen := e.policyWarned.LoadOrStore(entry+"\x00"+v.Kind+"\x00"+v.Ref, true); !seen {
		e.warnf(entry, stage, "%s", v)
	}
}

// committedSources lists every upstream source the entry's committed state
// pulls from: the chart, its distribution-manifests artifact and every
// locked image; or the artifact. A missing lock contributes nothing (the
// regen stage owns that complaint).
func committedSources(entry spec.Entry) []policySource {
	if entry.Kind == spec.KindArtifact {
		return []policySource{{spec.SourceArtifact, entry.Artifact.Artifact.Ref}}
	}
	m := entry.Chart
	sources := []policySource{{spec.SourceChart, spec.ChartSource(m.Chart.Repo, m.Chart.Name)}}
	if dm := m.Images.DistributionManifests; dm != nil && dm.Artifact != "" {
		artifact := dm.Artifact
		if appVersion, err := helmchart.AppVersion(vendorDir(entry), m.Chart.Name); err == nil {
			artifact = strings.ReplaceAll(artifact, "{appVersion}", appVersion)
		}
		sources = append(sources, policySource{spec.SourceArtifact, artifact})
	}
	if lock, err := spec.LoadImagesLock(entry.LockPath()); err == nil {
		for _, img := range lock.Images {
			sources = append(sources, policySource{spec.SourceImage, img.Source})
		}
	}
	return sources
}

// imageSources wraps discovered image references as policy sources.
func imageSources(refs []string) []policySource {
	out := make([]policySource, len(refs))
	for i, r := range refs {
		out[i] = policySource{spec.SourceImage, r}
	}
	return out
}
