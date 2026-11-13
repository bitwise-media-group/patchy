// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package spec

import (
	"fmt"
	"slices"
	"strings"

	"github.com/bitwise-media-group/patchy/internal/mirror/imageref"
)

// PolicyMode is what a source-policy violation does: fail blocks, warn
// narrates and lets the run continue.
type PolicyMode string

// The policy modes. An unset mode means fail.
const (
	PolicyFail PolicyMode = "fail"
	PolicyWarn PolicyMode = "warn"
)

// effective resolves the default (fail).
func (m PolicyMode) effective() PolicyMode {
	if m == "" {
		return PolicyFail
	}
	return m
}

// The source kinds a policy restricts.
const (
	// SourceChart is an upstream helm chart, identified by ChartSource.
	SourceChart = "chart"
	// SourceImage is a container image: a mirrored chart's image, or a
	// build's base image (FROM, COPY --from, RUN --mount from, the
	// # syntax= frontend).
	SourceImage = "image"
	// SourceArtifact is an upstream OCI artifact: an artifact entry's ref
	// or a chart's distribution-manifests artifact.
	SourceArtifact = "artifact"
)

// PolicyRules is one source kind's allow and deny lists of path globs
// (imageref.PathMatch: '*' stays within a '/'-separated segment, '**'
// crosses segments). A deny match always rejects; a non-empty allow list
// rejects everything none of its patterns match. Both empty restrict
// nothing.
type PolicyRules struct {
	Allow []string `yaml:"allow"`
	Deny  []string `yaml:"deny"`
}

// empty reports whether the rules restrict nothing.
func (r PolicyRules) empty() bool { return len(r.Allow) == 0 && len(r.Deny) == 0 }

// evaluate returns why the rules reject value, or "" when they permit it.
func (r PolicyRules) evaluate(value string) string {
	for _, p := range r.Deny {
		if imageref.PathMatch(p, value) {
			return fmt.Sprintf("denied by %q", p)
		}
	}
	if len(r.Allow) == 0 {
		return ""
	}
	for _, p := range r.Allow {
		if imageref.PathMatch(p, value) {
			return ""
		}
	}
	return "matches no allow pattern"
}

// MirrorPolicy is the patchy config's mirror.policy block: which upstream
// sources patchy mirror may pull charts, images and artifacts from.
type MirrorPolicy struct {
	// Mode is fail (default) or warn.
	Mode PolicyMode `yaml:"mode"`
	// Enforced pins this layer's policy: it is evaluated as written in the
	// file that declares it, alongside the merged policy, so a more
	// preferred config layer can tighten it but never loosen, downgrade or
	// delete it. Without it the block merges like any other key.
	Enforced bool `yaml:"enforced"`
	// Charts match "<chart.repo>/<chart.name>" with the scheme kept:
	// oci://ghcr.io/acme/charts/app, https://charts.example.com/app.
	Charts PolicyRules `yaml:"charts"`
	// Images match the canonical source repository (normalized, no tag
	// or digest): docker.io/library/nginx, registry.k8s.io/pause.
	Images PolicyRules `yaml:"images"`
	// Artifacts match the canonical artifact repository (no tag).
	Artifacts PolicyRules `yaml:"artifacts"`
}

// Layer converts the block into one evaluated policy layer.
func (p *MirrorPolicy) Layer(source string) PolicyLayer {
	return PolicyLayer{
		Source:   source,
		Mode:     p.Mode,
		Enforced: p.Enforced,
		Rules: map[string]PolicyRules{
			SourceChart:    p.Charts,
			SourceImage:    p.Images,
			SourceArtifact: p.Artifacts,
		},
	}
}

// BuildPolicy is the patchy config's build.policy block: which registries
// patchy build may pull base images from.
type BuildPolicy struct {
	// Mode is fail (default) or warn.
	Mode PolicyMode `yaml:"mode"`
	// Enforced pins this layer's policy, as for MirrorPolicy.
	Enforced bool `yaml:"enforced"`
	// Images match every image a Dockerfile pulls — FROM, COPY --from,
	// RUN --mount from=, and the # syntax= frontend — as the canonical
	// repository (normalized, no tag or digest).
	Images PolicyRules `yaml:"images"`
}

// Layer converts the block into one evaluated policy layer.
func (p *BuildPolicy) Layer(source string) PolicyLayer {
	return PolicyLayer{
		Source:   source,
		Mode:     p.Mode,
		Enforced: p.Enforced,
		Rules:    map[string]PolicyRules{SourceImage: p.Images},
	}
}

// PolicyLayer is one policy evaluated on its own: the merged config's, or
// an enforced layer's as written in its file.
type PolicyLayer struct {
	// Source names where the layer came from, for messages.
	Source string
	Mode   PolicyMode
	// Enforced marks a layer pinned by `enforced: true`.
	Enforced bool
	// Rules are keyed by source kind (SourceChart, SourceImage,
	// SourceArtifact); a kind with no rules is unrestricted.
	Rules map[string]PolicyRules
}

// SourcePolicy is the resolved source policy one command enforces: every
// layer must permit a source. Zero value restricts nothing.
type SourcePolicy struct {
	Layers []PolicyLayer
}

// Restricts reports whether any layer restricts kind.
func (p SourcePolicy) Restricts(kind string) bool {
	for _, l := range p.Layers {
		if !l.Rules[kind].empty() {
			return true
		}
	}
	return false
}

// PolicyViolation is one source a policy rejected.
type PolicyViolation struct {
	Kind string `json:"kind"`
	// Ref is the reference as given (before canonicalization).
	Ref string `json:"ref"`
	// Value is what the patterns were matched against.
	Value string `json:"value"`
	// Source names the rejecting layer.
	Source string     `json:"source"`
	Mode   PolicyMode `json:"mode"`
	Reason string     `json:"reason"`
}

// Blocking reports whether the violation fails the run.
func (v PolicyViolation) Blocking() bool { return v.Mode == PolicyFail }

// String renders the violation for messages.
func (v PolicyViolation) String() string {
	subject := v.Value
	if v.Ref != v.Value {
		subject = fmt.Sprintf("%s (%s)", v.Value, v.Ref)
	}
	return fmt.Sprintf("source policy: %s %s is not permitted: %s (%s.allow/deny in %s)",
		v.Kind, subject, v.Reason, policyKey(v.Kind), v.Source)
}

// policyKey is the config key a kind's rules live under.
func policyKey(kind string) string { return kind + "s" }

// Check evaluates one source against every layer. It returns nil when all
// layers permit it; otherwise the most severe rejection (a fail layer over
// a warn one, earlier layers first — enforced layers lead), so a source
// rejected by several layers is reported once.
func (p SourcePolicy) Check(kind, ref string) *PolicyViolation {
	value := CanonicalSource(kind, ref)
	var worst *PolicyViolation
	for _, l := range p.Layers {
		reason := l.Rules[kind].evaluate(value)
		if reason == "" {
			continue
		}
		v := &PolicyViolation{Kind: kind, Ref: ref, Value: value, Source: l.Source,
			Mode: l.Mode.effective(), Reason: reason}
		if worst == nil || (v.Blocking() && !worst.Blocking()) {
			worst = v
		}
	}
	return worst
}

// Reject reports a source that cannot be evaluated at all (a reference
// that does not resolve) as a violation of the strictest layer restricting
// kind: it cannot be proven permitted. Nil when no layer restricts kind.
func (p SourcePolicy) Reject(kind, ref, reason string) *PolicyViolation {
	var worst *PolicyViolation
	for _, l := range p.Layers {
		if l.Rules[kind].empty() {
			continue
		}
		v := &PolicyViolation{Kind: kind, Ref: ref, Value: ref, Source: l.Source,
			Mode: l.Mode.effective(), Reason: reason}
		if worst == nil || (v.Blocking() && !worst.Blocking()) {
			worst = v
		}
	}
	return worst
}

// CanonicalSource is the string policy patterns match for a reference:
// images and artifacts lose their tag and digest and gain the implicit
// docker.io/library/ host; charts (already ChartSource form) pass through.
func CanonicalSource(kind, ref string) string {
	if kind == SourceChart {
		return ref
	}
	ref = imageref.Normalize(strings.TrimPrefix(ref, "oci://"))
	if r, err := imageref.Parse(ref); err == nil {
		return r.Repository
	}
	return ref
}

// ChartSource is a chart's policy identity: the repo with the chart name
// appended, the scheme kept (oci://host/path/name, https://host/path/name).
func ChartSource(repo, name string) string {
	return strings.TrimSuffix(repo, "/") + "/" + name
}

// Validate checks every layer: a known mode and well-formed patterns, so a
// pattern that could never match (a tag, a missing registry host, a
// schemeless chart) fails at load instead of silently restricting nothing.
func (p SourcePolicy) Validate() error {
	for _, l := range p.Layers {
		at := "policy (" + l.Source + ")"
		switch l.Mode {
		case "", PolicyFail, PolicyWarn:
		default:
			return fmt.Errorf("%s: mode must be fail or warn (got %q)", at, l.Mode)
		}
		kinds := make([]string, 0, len(l.Rules))
		for k := range l.Rules {
			kinds = append(kinds, k)
		}
		slices.Sort(kinds)
		for _, kind := range kinds {
			r := l.Rules[kind]
			for _, list := range []struct {
				name     string
				patterns []string
			}{{"allow", r.Allow}, {"deny", r.Deny}} {
				for i, pattern := range list.patterns {
					if err := validatePattern(kind, pattern); err != nil {
						return fmt.Errorf("%s: %s.%s[%d]: %w", at, policyKey(kind), list.name, i, err)
					}
				}
			}
		}
	}
	return nil
}

// validatePattern rejects patterns that can never match their kind's
// canonical form.
func validatePattern(kind, pattern string) error {
	if strings.TrimSpace(pattern) == "" {
		return fmt.Errorf("empty pattern")
	}
	if pattern == "*" {
		// Every canonical source spans several segments, so a lone '*'
		// (one segment) would silently match nothing.
		return fmt.Errorf("%q matches a single path segment and so nothing; use \"**\" to match everything", pattern)
	}
	if kind == SourceChart {
		if wildcardOnly(pattern) || strings.HasPrefix(pattern, "oci://") || strings.HasPrefix(pattern, "https://") {
			return nil
		}
		return fmt.Errorf("%q must start with oci:// or https:// (charts match <chart.repo>/<chart.name>)", pattern)
	}
	if strings.HasPrefix(pattern, "oci://") {
		return fmt.Errorf("%q: drop the oci:// scheme (%s patterns match the bare repository)", pattern, policyKey(kind))
	}
	if strings.Contains(pattern, "@") {
		return fmt.Errorf("%q carries a digest; patterns match the repository only", pattern)
	}
	segments := strings.Split(pattern, "/")
	if last := segments[len(segments)-1]; strings.Contains(last, ":") {
		return fmt.Errorf("%q carries a tag; patterns match the repository only", pattern)
	}
	first := segments[0]
	if !wildcardOnly(first) && !strings.ContainsAny(first, ".:") && first != "localhost" {
		return fmt.Errorf("%q has no registry host; write the canonical form "+
			"(docker.io/library/nginx, not nginx or library/nginx)", pattern)
	}
	return nil
}

// wildcardOnly reports a pattern segment made only of '*'.
func wildcardOnly(s string) bool { return s != "" && strings.Trim(s, "*") == "" }
