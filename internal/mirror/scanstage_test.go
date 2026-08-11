// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package mirror

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bitwise-media-group/patchy/internal/mirror/scan"
	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
)

// fakeScanner returns canned findings for every ref.
type fakeScanner struct {
	name     string
	findings []scan.Finding
	scanned  []string
}

func (s *fakeScanner) Name() string { return s.name }

func (s *fakeScanner) ScanImage(_ context.Context, ref string) ([]scan.Finding, error) {
	s.scanned = append(s.scanned, ref)
	return s.findings, nil
}

// scanEngine rebuilds the fixture engine with an injected scanner roster.
func scanEngine(f *fixture, scanners ...scan.ImageScanner) *Engine {
	f.t.Helper()
	global, err := spec.LoadConfig(filepath.Join(f.root, "mirror.yaml"))
	if err != nil {
		f.t.Fatal(err)
	}
	f.eng = New(Config{
		Root:          f.root,
		Global:        global,
		Now:           func() time.Time { return testNow },
		ImageScanners: scanners,
	})
	return f.eng
}

// scanFixture builds a converged single-chart store.
func scanFixture(t *testing.T) (*fixture, spec.Entry) {
	t.Helper()
	f := newFixture(t)
	app := appCanonical
	f.pushImage(f.host+"/apps/app:1.0.0", testNow.AddDate(0, -1, 0))
	f.pushChart(f.host+"/upstream", "demo", "1.0.0", f.chartTgz("demo", "1.0.0", "1.0.0", app+":1.0.0"))
	f.chartManifest("demo", "")
	f.write("charts/demo/values/discovery.yaml", "{}\n")
	eng := f.engine()
	entry, err := eng.Entry("demo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Upgrade(context.Background(), entry, ""); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	return f, entry
}

var criticalFinding = scan.Finding{
	ID: "CVE-2026-1111", Aliases: []string{"GHSA-aaaa-bbbb"}, Severity: "CRITICAL",
	Package: "libfoo", Installed: "1.0.0", FixedIn: []string{"1.0.1"}, Scanner: "fake",
}

func TestEngineScanGate(t *testing.T) {
	f, entry := scanFixture(t)
	ctx := context.Background()

	t.Run("blocking finding fails the gate", func(t *testing.T) {
		fake := &fakeScanner{name: "fake", findings: []scan.Finding{
			criticalFinding,
			{ID: "CVE-2026-2222", Severity: "LOW", Package: "libbar", Installed: "2.0.0", FixedIn: []string{"2.1.0"}},
			{ID: "CVE-2026-3333", Severity: "CRITICAL", Package: "libunfixed", Installed: "1.0.0"},
		}}
		eng := scanEngine(f, fake)
		report, err := eng.Scan(ctx, entry)
		if err != nil {
			t.Fatalf("Scan: %v", err)
		}
		// LOW is under the gate; the unfixed CRITICAL drops under
		// ignoreUnfixed; the fixed CRITICAL blocks.
		if len(report.Blocking) != 1 || report.Blocking[0].ID != "CVE-2026-1111" {
			t.Errorf("blocking = %+v", report.Blocking)
		}
		if !report.Failed() {
			t.Error("gate should fail")
		}
		if len(fake.scanned) != 1 || !strings.Contains(fake.scanned[0], "@sha256:") {
			t.Errorf("scanned refs = %v", fake.scanned)
		}
	})

	t.Run("allowlist alias suppresses", func(t *testing.T) {
		// The allowlist entry names the GHSA alias, not the CVE id —
		// matching must cross id families.
		f.write("charts/demo/security/allowlist.yaml", `vulnerabilities:
  - id: GHSA-aaaa-bbbb
    statement: accepted
    expired_at: "2026-10-01"
`)
		eng := scanEngine(f, &fakeScanner{name: "fake", findings: []scan.Finding{criticalFinding}})
		report, err := eng.Scan(ctx, entry)
		if err != nil {
			t.Fatalf("Scan: %v", err)
		}
		if len(report.Blocking) != 0 || report.Suppressed != 1 {
			t.Errorf("report = %+v", report)
		}
		if report.Failed() {
			t.Error("gate should pass")
		}
	})
}

func TestEngineDeriveAllowlist(t *testing.T) {
	f, _ := scanFixture(t)
	ctx := context.Background()

	// Opt the chart into generation with a preamble.
	manifest := f.read("charts/demo/manifest.yaml")
	manifest = strings.Replace(manifest, "publish:", `scan:
  allowlist:
    generate: true
    preamble: |
      Context for reviewers.
publish:`, 1)
	f.write("charts/demo/manifest.yaml", manifest)

	eng := scanEngine(f, &fakeScanner{name: "fake", findings: []scan.Finding{criticalFinding}})
	entry, err := eng.Entry("demo")
	if err != nil {
		t.Fatal(err)
	}
	res, err := eng.DeriveAllowlist(ctx, entry)
	if err != nil {
		t.Fatalf("DeriveAllowlist: %v", err)
	}
	if !res.Generated || res.Added != 1 || res.Kept != 0 {
		t.Errorf("result = %+v", res)
	}
	raw := f.read("charts/demo/security/allowlist.yaml")
	if !strings.Contains(raw, "CVE-2026-1111") || !strings.Contains(raw, "# Context for reviewers.") {
		t.Errorf("allowlist:\n%s", raw)
	}
	if !strings.Contains(raw, `expired_at: "2026-11-09"`) { // testNow + 90d
		t.Errorf("new expiry:\n%s", raw)
	}

	// After derivation, the gate passes.
	report, err := eng.Scan(ctx, entry)
	if err != nil {
		t.Fatal(err)
	}
	if report.Failed() {
		t.Errorf("gate after derivation = %+v", report)
	}
}

func TestEngineValidate(t *testing.T) {
	f, entry := scanFixture(t)
	ctx := context.Background()
	eng := scanEngine(f, &fakeScanner{name: "fake"})

	t.Run("clean store validates", func(t *testing.T) {
		res, err := eng.Validate(ctx, entry, nil)
		if err != nil {
			t.Fatalf("Validate: %v", err)
		}
		if res.Failed() {
			t.Errorf("result = %+v (regen %v, lint %v, err %q)", res, res.RegenDiffs, res.Lint, res.Err)
		}
		// The chart rule defaults to provider none in the fixture: a gap.
		if res.Verify == nil || len(res.Verify.Gaps) == 0 {
			t.Errorf("verify = %+v", res.Verify)
		}
	})

	t.Run("stale lock is caught", func(t *testing.T) {
		lockPath := "charts/demo/images.lock.yaml"
		original := f.read(lockPath)
		f.write(lockPath, strings.Replace(original, "sha256:", "sha256:0000", 1))
		res, err := eng.Validate(ctx, entry, []string{StageRegen})
		if err != nil {
			t.Fatalf("Validate: %v", err)
		}
		if len(res.RegenDiffs) == 0 {
			t.Error("stale lock not detected")
		}
		f.write(lockPath, original)
	})

	t.Run("expired allowlist entry fails lint", func(t *testing.T) {
		f.write("charts/demo/security/allowlist.yaml", `vulnerabilities:
  - id: CVE-1
    statement: s
    expired_at: "2026-01-01"
  - id: CVE-2
    statement: s
    expired_at: "2027-08-11"
`)
		res, err := eng.Validate(ctx, entry, []string{StageLint})
		if err != nil {
			t.Fatalf("Validate: %v", err)
		}
		if len(res.Lint) != 2 {
			t.Errorf("lint = %v", res.Lint)
		}
		joined := strings.Join(res.Lint, "\n")
		if !strings.Contains(joined, "expired on") || !strings.Contains(joined, "more than 90 days") {
			t.Errorf("lint = %v", res.Lint)
		}
	})

	t.Run("non-mirror oci ref fails lint", func(t *testing.T) {
		rendered := f.read("charts/demo/rendered/manifests.yaml")
		f.write("charts/demo/rendered/manifests.yaml",
			rendered+"---\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n"+
				"data:\n  ref: oci://ghcr.io/outside/thing:v1\n")
		res, err := eng.Validate(ctx, entry, []string{StageLint})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, l := range res.Lint {
			if strings.Contains(l, "outside the mirror") {
				found = true
			}
		}
		if !found {
			t.Errorf("lint = %v", res.Lint)
		}
		f.write("charts/demo/rendered/manifests.yaml", rendered)
	})
}
