// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package mirror

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/bitwise-media-group/patchy/internal/mirror/imageref"
	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
	"github.com/bitwise-media-group/patchy/internal/mirror/yamledit"
)

// Lint checks one entry's manifest and allowlist against store policy.
// Schema-level validity (kinds, name/dir match, unknown fields) is already
// enforced at load time; lint covers the rules a well-formed file can
// still break.
func (e *Engine) Lint(entry spec.Entry) ([]string, error) {
	var issues []string
	fail := func(format string, args ...any) {
		issues = append(issues, fmt.Sprintf(format, args...))
	}

	if entry.Kind == spec.KindChart {
		e.lintChart(entry, fail)
	} else {
		e.lintArtifact(entry, fail)
	}
	if err := e.lintAllowlist(entry, fail); err != nil {
		return nil, err
	}
	return issues, nil
}

// lintChart covers chart-specific policy.
func (e *Engine) lintChart(entry spec.Entry, fail func(string, ...any)) {
	m := entry.Chart
	if m.Chart.Repo == "" || m.Chart.Name == "" || m.Chart.Version == "" {
		fail("chart.repo, chart.name and chart.version are required")
	}
	if m.Chart.Repo != "" && !strings.HasPrefix(m.Chart.Repo, "oci://") &&
		!strings.HasPrefix(m.Chart.Repo, "https://") {
		fail("chart.repo must be an oci:// or https:// URL (got %q)", m.Chart.Repo)
	}
	lintProvider("chart.verifyUpstream", m.Chart.VerifyUpstream, fail)

	// Tracked images: the tag is machine-picked into a values file, so
	// the rule must name both an image and a path that already resolves
	// there — the pick replaces a value, it never invents one.
	for i, rule := range m.Images.Track {
		prefix := fmt.Sprintf("images.track[%d]", i)
		if rule.Image == "" {
			fail("%s missing image", prefix)
		}
		if rule.ValuesPath == "" {
			fail("%s missing valuesPath", prefix)
			continue
		}
		if rule.CooldownDays != nil && *rule.CooldownDays < 0 {
			fail("%s cooldownDays must be non-negative", prefix)
		}
		valuesFile := rule.ValuesFile
		if valuesFile == "" && len(m.Discovery.ValuesFiles) > 0 {
			valuesFile = m.Discovery.ValuesFiles[0]
		}
		if valuesFile == "" {
			fail("%s has no valuesFile and the chart declares no discovery.valuesFiles", prefix)
			continue
		}
		raw, err := os.ReadFile(filepath.Join(entry.Dir, valuesFile))
		if err != nil {
			fail("%s valuesFile %q not found", prefix, valuesFile)
			continue
		}
		if _, err := yamledit.Get(raw, rule.ValuesPath); err != nil {
			fail("%s valuesPath %q does not resolve in %s", prefix, rule.ValuesPath, valuesFile)
		}
	}

	// Every locked image must be coverable: at least one rule, all valid.
	if len(m.Images.VerifyUpstream) == 0 {
		fail("images.verifyUpstream must declare at least one rule (use provider: none to document a gap)")
	}
	for i, rule := range m.Images.VerifyUpstream {
		prefix := fmt.Sprintf("images.verifyUpstream[%d]", i)
		if rule.Match == "" {
			fail("%s missing match pattern", prefix)
		}
		lintProvider(prefix, rule.VerifyRule, fail)
	}

	e.lintOCIRefs(entry, fail)
}

// lintProvider validates one verifyUpstream rule's provider shape.
func lintProvider(prefix string, rule spec.VerifyRule, fail func(string, ...any)) {
	switch rule.EffectiveProvider() {
	case "none":
	case "cosign-keyless":
		if rule.CertificateIdentityRegexp == "" {
			fail("%s: cosign-keyless requires certificateIdentityRegexp", prefix)
		}
	case "cosign-key":
		if rule.Key == "" {
			fail("%s: cosign-key requires key", prefix)
		}
	default:
		fail("%s: unknown provider %q", prefix, rule.Provider)
	}
}

// lintArtifact covers artifact-specific policy.
func (e *Engine) lintArtifact(entry spec.Entry, fail func(string, ...any)) {
	a := entry.Artifact
	if a.Artifact.Ref == "" || a.Artifact.Version == "" {
		fail("artifact.ref and artifact.version are required")
	}
	lintProvider("artifact.verifyUpstream", a.Artifact.VerifyUpstream, fail)
	switch a.Scan.EffectiveEnabled() {
	case "auto", "true", "false":
	default:
		fail("scan.enabled must be auto, true or false (got %q)", a.Scan.Enabled)
	}
}

// lintOCIRefs enforces the mirror boundary on rendered output: an oci://
// scalar in the rendered manifests is a registry pull the platform will
// make at runtime, and anything outside the mirror bypasses the review
// gate this store exists to be. Escapes need a reasoned allowance.
func (e *Engine) lintOCIRefs(entry spec.Entry, fail func(string, ...any)) {
	raw, err := os.ReadFile(renderedPath(entry))
	if err != nil {
		// Not rendered yet; the regen stage owns that complaint.
		return
	}
	allow := entry.Chart.OCIRefs.Allow
	for i, a := range allow {
		if a.Pattern == "" {
			fail("ociRefs.allow[%d] missing pattern", i)
		}
		if a.Reason == "" {
			fail("ociRefs.allow[%d] missing reason (why may this bypass the mirror?)", i)
		}
	}
	mirrorPrefix := "oci://" + e.global.Registry.URL
	for _, ref := range ociRefsIn(raw) {
		if strings.HasPrefix(ref, mirrorPrefix) {
			continue
		}
		allowed := false
		for _, a := range allow {
			if a.Pattern != "" && imageref.GlobMatch(a.Pattern, ref) {
				allowed = true
				break
			}
		}
		if !allowed {
			fail("rendered manifests reference %q outside the mirror "+
				"(allow via ociRefs.allow with a reason, or fix the values)", ref)
		}
	}
}

// ociRefsIn collects whole string scalars starting with oci:// across all
// documents — whole scalars only, which skips prose mentions in CRD
// descriptions.
func ociRefsIn(rendered []byte) []string {
	var refs []string
	seen := map[string]bool{}
	dec := yaml.NewDecoder(strings.NewReader(string(rendered)))
	for {
		var doc any
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// Rendered output that does not parse is regen's problem.
			return refs
		}
		walkStrings(doc, func(s string) {
			if strings.HasPrefix(s, "oci://") && !seen[s] {
				seen[s] = true
				refs = append(refs, s)
			}
		})
	}
	return refs
}

// walkStrings visits every string scalar in a decoded YAML value.
func walkStrings(node any, visit func(string)) {
	switch n := node.(type) {
	case string:
		visit(n)
	case map[string]any:
		for _, v := range n {
			walkStrings(v, visit)
		}
	case []any:
		for _, v := range n {
			walkStrings(v, visit)
		}
	}
}

// lintAllowlist enforces the accepted-risk policy: every entry carries a
// statement and an expiry, expiries are in the future (re-review on
// schedule is the whole forcing function) and within the policy horizon.
func (e *Engine) lintAllowlist(entry spec.Entry, fail func(string, ...any)) error {
	allow, err := spec.LoadAllowlist(entry.Dir)
	if err != nil {
		return err
	}
	today := e.now()
	horizon := today.AddDate(0, 0, e.global.Scan.EffectiveAllowlistMaxDays())
	for i, a := range allow.Vulnerabilities {
		if a.ID == "" {
			fail("allowlist entry %d missing id", i)
			continue
		}
		if a.Statement == "" {
			fail("allowlist %s missing statement", a.ID)
		}
		if a.ExpiredAt == "" {
			fail("allowlist %s missing expired_at", a.ID)
			continue
		}
		expiry, err := time.Parse("2006-01-02", a.ExpiredAt)
		if err != nil {
			fail("allowlist %s has unparseable expired_at %q", a.ID, a.ExpiredAt)
			continue
		}
		if !expiry.After(today) {
			fail("allowlist %s expired on %s — re-review required", a.ID, a.ExpiredAt)
		}
		if expiry.After(horizon) {
			fail("allowlist %s expires more than %d days out", a.ID, e.global.Scan.EffectiveAllowlistMaxDays())
		}
	}
	return nil
}
