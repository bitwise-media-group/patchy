// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package mirror

import (
	"fmt"
	"strings"
	"testing"

	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
)

func TestLintProvider(t *testing.T) {
	tests := []struct {
		name string
		rule spec.VerifyRule
		want string // "" = no issue
	}{
		{"none is a documented gap", spec.VerifyRule{Provider: "none"}, ""},
		{"empty defaults to none", spec.VerifyRule{}, ""},
		{"keyless without regexp", spec.VerifyRule{Provider: "cosign-keyless"}, "requires certificateIdentityRegexp"},
		{"keyless with regexp", spec.VerifyRule{Provider: "cosign-keyless", CertificateIdentityRegexp: ".*"}, ""},
		{"key without key", spec.VerifyRule{Provider: "cosign-key"}, "requires key"},
		{"key with key", spec.VerifyRule{Provider: "cosign-key", Key: "cosign.pub"}, ""},
		{"unknown provider", spec.VerifyRule{Provider: "gpg"}, `unknown provider "gpg"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var issues []string
			lintProvider("rule", tt.rule, func(format string, args ...any) {
				issues = append(issues, fmt.Sprintf(format, args...))
			})
			if tt.want == "" {
				if len(issues) != 0 {
					t.Fatalf("issues = %v, want none", issues)
				}
				return
			}
			if len(issues) != 1 || !strings.Contains(issues[0], tt.want) {
				t.Fatalf("issues = %v, want one containing %q", issues, tt.want)
			}
		})
	}
}

// trackManifest builds a chart manifest with the given discovery and
// images blocks; the chart pin itself is always well-formed.
func trackManifest(discovery, images string) string {
	return fmt.Sprintf(`apiVersion: mirror.patchy.bitwisemedia.uk/v1alpha1
kind: Chart
name: demo
chart:
  repo: oci://reg.example.test/upstream
  name: demo
  version: "1.0.0"
%s%s`, discovery, images)
}

// TestLintTrackRules pins the images.track policy — in particular the
// valuesFile fallback and valuesPath resolution, which are re-implemented
// by the pick path (tracktags.go): a rule lint blesses must be one
// ApplyTracks can execute, or lint green means nothing.
func TestLintTrackRules(t *testing.T) {
	discovery := "discovery:\n  valuesFiles: [values/discovery.yaml]\n"
	verify := "  verifyUpstream:\n    - match: \"*\"\n      provider: none\n"
	tests := []struct {
		name      string
		discovery string
		images    string
		values    string // values/discovery.yaml content; "" = none written
		want      []string
	}{
		{
			name:      "fallback file and resolving path lint clean",
			discovery: discovery,
			images: "images:\n  track:\n    - image: reg.example.test/apps/runner\n" +
				"      valuesPath: .image\n" + verify,
			values: "image: reg.example.test/apps/runner:1.0.0\n",
			want:   nil,
		},
		{
			name:      "missing image and path",
			discovery: discovery,
			images:    "images:\n  track:\n    - versionConstraint: \">=1.0.0\"\n" + verify,
			values:    "{}\n",
			want:      []string{"images.track[0] missing image", "images.track[0] missing valuesPath"},
		},
		{
			name:      "negative cooldown",
			discovery: discovery,
			images: "images:\n  track:\n    - image: reg.example.test/apps/runner\n" +
				"      valuesPath: .image\n      cooldownDays: -1\n" + verify,
			values: "image: reg.example.test/apps/runner:1.0.0\n",
			want:   []string{"cooldownDays must be non-negative"},
		},
		{
			name:      "no values file anywhere",
			discovery: "",
			images: "images:\n  track:\n    - image: reg.example.test/apps/runner\n" +
				"      valuesPath: .image\n" + verify,
			values: "",
			want:   []string{"no valuesFile and the chart declares no discovery.valuesFiles"},
		},
		{
			name:      "unresolvable values path",
			discovery: discovery,
			images: "images:\n  track:\n    - image: reg.example.test/apps/runner\n" +
				"      valuesPath: .deep.image\n" + verify,
			values: "image: reg.example.test/apps/runner:1.0.0\n",
			want:   []string{`valuesPath ".deep.image" does not resolve`},
		},
		{
			name:      "declared values file absent",
			discovery: discovery,
			images: "images:\n  track:\n    - image: reg.example.test/apps/runner\n" +
				"      valuesPath: .image\n" + verify,
			values: "",
			want:   []string{`valuesFile "values/discovery.yaml" not found`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			f.write("charts/demo/manifest.yaml", trackManifest(tt.discovery, tt.images))
			if tt.values != "" {
				f.write("charts/demo/values/discovery.yaml", tt.values)
			}
			eng := f.engine()
			entry, err := eng.Entry("demo")
			if err != nil {
				t.Fatal(err)
			}
			issues, err := eng.Lint(entry)
			if err != nil {
				t.Fatalf("Lint: %v", err)
			}
			if len(issues) != len(tt.want) {
				t.Fatalf("issues = %v, want %d: %v", issues, len(tt.want), tt.want)
			}
			for i, want := range tt.want {
				if !strings.Contains(issues[i], want) {
					t.Errorf("issue[%d] = %q, want one containing %q", i, issues[i], want)
				}
			}
		})
	}
}

// TestLintChartBasics pins the structural chart rules.
func TestLintChartBasics(t *testing.T) {
	f := newFixture(t)
	f.write("charts/demo/manifest.yaml", `apiVersion: mirror.patchy.bitwisemedia.uk/v1alpha1
kind: Chart
name: demo
chart:
  repo: git://reg.example.test/upstream
  name: demo
  version: "1.0.0"
images:
  verifyUpstream:
    - provider: none
`)
	eng := f.engine()
	entry, err := eng.Entry("demo")
	if err != nil {
		t.Fatal(err)
	}
	issues, err := eng.Lint(entry)
	if err != nil {
		t.Fatalf("Lint: %v", err)
	}
	joined := strings.Join(issues, "\n")
	for _, want := range []string{
		"chart.repo must be an oci:// or https:// URL",
		"images.verifyUpstream[0] missing match pattern",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("issues lack %q:\n%s", want, joined)
		}
	}

	// No verifyUpstream rules at all is its own refusal: silence about
	// provenance is never the default.
	f.write("charts/demo/manifest.yaml", `apiVersion: mirror.patchy.bitwisemedia.uk/v1alpha1
kind: Chart
name: demo
chart:
  repo: oci://reg.example.test/upstream
  name: demo
  version: "1.0.0"
`)
	entry, err = f.engine().Entry("demo")
	if err != nil {
		t.Fatal(err)
	}
	issues, err = f.eng.Lint(entry)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || !strings.Contains(issues[0], "at least one rule") {
		t.Errorf("issues = %v", issues)
	}
}

// TestLintOCIRefsAllow pins the mirror-boundary escape hatch from both
// sides: a reasoned allow pattern must actually permit the escape (the
// documented bypass has to work), and allow entries missing their pattern
// or reason are themselves violations.
func TestLintOCIRefsAllow(t *testing.T) {
	f := newFixture(t)
	f.write("charts/demo/manifest.yaml", `apiVersion: mirror.patchy.bitwisemedia.uk/v1alpha1
kind: Chart
name: demo
chart:
  repo: oci://reg.example.test/upstream
  name: demo
  version: "1.0.0"
images:
  verifyUpstream:
    - match: "*"
      provider: none
ociRefs:
  allow:
    - pattern: "oci://ghcr.example.test/allowed/*"
      reason: upstream operator pulls its own bundle
    - reason: no pattern here
    - pattern: "oci://ghcr.example.test/unreasoned/*"
`)
	f.write("charts/demo/rendered/manifests.yaml", `apiVersion: v1
kind: ConfigMap
metadata:
  name: x
data:
  allowed: oci://ghcr.example.test/allowed/bundle:1.0.0
  escaped: oci://ghcr.example.test/other/bundle:1.0.0
`)
	eng := f.engine()
	entry, err := eng.Entry("demo")
	if err != nil {
		t.Fatal(err)
	}
	issues, err := eng.Lint(entry)
	if err != nil {
		t.Fatalf("Lint: %v", err)
	}
	joined := strings.Join(issues, "\n")
	if strings.Contains(joined, "allowed/bundle") {
		t.Errorf("allowed ref still flagged:\n%s", joined)
	}
	for _, want := range []string{
		"ociRefs.allow[1] missing pattern",
		"ociRefs.allow[2] missing reason",
		`"oci://ghcr.example.test/other/bundle:1.0.0" outside the mirror`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("issues lack %q:\n%s", want, joined)
		}
	}
}

// TestLintOCIRefsMultiRegistry pins the mirror boundary over several
// registries: a rendered oci:// ref under ANY configured registry is inside
// the mirror; one outside all of them is an escape.
func TestLintOCIRefsMultiRegistry(t *testing.T) {
	f := newFixture(t)
	f.setRegistries(fmt.Sprintf("  - name: a\n    url: %[1]s/mirror-a\n"+
		"  - name: b\n    url: %[1]s/mirror-b\n", f.host))
	f.write("charts/demo/manifest.yaml", `apiVersion: mirror.patchy.bitwisemedia.uk/v1alpha1
kind: Chart
name: demo
chart:
  repo: oci://reg.example.test/upstream
  name: demo
  version: "1.0.0"
images:
  verifyUpstream:
    - match: "*"
      provider: none
`)
	f.write("charts/demo/rendered/manifests.yaml", fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: x
data:
  second: oci://%[1]s/mirror-b/charts/other:1.0.0
  escaped: oci://elsewhere.example.test/bundle:1.0.0
`, f.host))
	eng := f.engine()
	entry, err := eng.Entry("demo")
	if err != nil {
		t.Fatal(err)
	}
	issues, err := eng.Lint(entry)
	if err != nil {
		t.Fatalf("Lint: %v", err)
	}
	joined := strings.Join(issues, "\n")
	if strings.Contains(joined, "mirror-b/charts/other") {
		t.Errorf("second-registry ref flagged as an escape:\n%s", joined)
	}
	if !strings.Contains(joined, `"oci://elsewhere.example.test/bundle:1.0.0" outside the mirror`) {
		t.Errorf("outside-both ref not flagged:\n%s", joined)
	}
}

// TestLintAllowlistShape pins the per-entry allowlist requirements the
// expiry tests do not cover.
func TestLintAllowlistShape(t *testing.T) {
	f := newFixture(t)
	f.chartManifest("demo", "")
	f.write("charts/demo/security/allowlist.yaml", `vulnerabilities:
  - statement: no id at all
    expired_at: "2026-10-01"
  - id: CVE-1
    expired_at: "2026-10-01"
  - id: CVE-2
    statement: s
  - id: CVE-3
    statement: s
    expired_at: "soon"
`)
	eng := f.engine()
	entry, err := eng.Entry("demo")
	if err != nil {
		t.Fatal(err)
	}
	issues, err := eng.Lint(entry)
	if err != nil {
		t.Fatalf("Lint: %v", err)
	}
	joined := strings.Join(issues, "\n")
	for _, want := range []string{
		"allowlist entry 0 missing id",
		"allowlist CVE-1 missing statement",
		"allowlist CVE-2 missing expired_at",
		`allowlist CVE-3 has unparseable expired_at "soon"`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("issues lack %q:\n%s", want, joined)
		}
	}
}
