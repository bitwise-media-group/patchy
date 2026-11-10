// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package mirror

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bitwise-media-group/patchy/internal/mirror/scan"
	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
)

// artifactManifest writes the "bundle" artifact entry pinned at 1.0.0.
// extraBlocks lands after the artifact block (scan:, publish:).
func (f *fixture) artifactManifest(canonicalRef, extraBlocks string) {
	manifest := fmt.Sprintf(`apiVersion: mirror.patchy.bitwisemedia.uk/v1alpha1
kind: Artifact
name: bundle
artifact:
  ref: %s
  version: "1.0.0"
  versionConstraint: ">=1.0.0 <2.0.0"
  verifyUpstream:
    provider: none
%s`, canonicalRef, extraBlocks)
	f.write(filepath.Join("artifacts", "bundle", "manifest.yaml"), manifest)
}

// TestEngineArtifactLifecycle drives a kind: Artifact entry through
// upgrade → sync → drift, the full path no chart entry exercises: the
// artifact lock and publish-target composition, the artifact sync branch,
// the idempotent re-run, and the self-healing re-convergence after someone
// moves the mirror tag off the locked digest.
func TestEngineArtifactLifecycle(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	keyPath, pubPath, password := keyPair(t)

	canonical := "example.test/bundles/bundle"
	digest := f.pushImage(f.host+"/bundles/bundle:1.0.0", testNow.AddDate(0, -1, 0))
	f.artifactManifest(canonical, "")
	eng := keyedEngine(f, keyPath, pubPath, password)

	entry, err := eng.Entry("bundle")
	if err != nil {
		t.Fatal(err)
	}
	target := f.host + "/mirror/artifacts/" + canonical + ":1.0.0"

	t.Run("upgrade locks the pinned version", func(t *testing.T) {
		assertArtifactUpgrade(ctx, t, eng, entry, canonical, digest, target)
	})
	t.Run("sync publishes signed and is idempotent", func(t *testing.T) {
		assertArtifactSync(ctx, t, f, eng, entry, digest, target)
	})
	t.Run("sync re-converges a drifted target", func(t *testing.T) {
		assertArtifactDrift(ctx, t, f, eng, entry, digest, target)
	})
}

// assertArtifactUpgrade checks the artifact lock and its publish target.
func assertArtifactUpgrade(ctx context.Context, t *testing.T, eng *Engine, entry spec.Entry,
	canonical, digest, target string) {
	t.Helper()
	res, err := eng.Upgrade(ctx, entry, "")
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if len(res.Changed) == 0 {
		t.Errorf("result = %+v, want the lock written", res)
	}
	lock, err := spec.LoadArtifactLock(entry.LockPath())
	if err != nil {
		t.Fatal(err)
	}
	if lock.Artifact.Ref != canonical || lock.Artifact.Version != "1.0.0" || lock.Artifact.Digest != digest {
		t.Errorf("lock = %+v", lock.Artifact)
	}
	// The publish target composes registry URL + artifactNamespace +
	// canonical path; a mistake here misroutes every publish.
	if lock.Artifact.Targets["primary"] != target {
		t.Errorf("target = %q, want %q", lock.Artifact.Targets["primary"], target)
	}
}

// assertArtifactSync checks the first publish and the idempotent re-run.
func assertArtifactSync(ctx context.Context, t *testing.T, f *fixture, eng *Engine, entry spec.Entry,
	digest, target string) {
	t.Helper()
	res, err := eng.Sync(ctx, entry, SyncOptions{})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(res.Records) != 1 || res.Records[0].Action != ActionPushed || !res.Records[0].Signed {
		t.Fatalf("records = %+v", res.Records)
	}
	got, err := f.eng.reg.Digest(ctx, target)
	if err != nil || got != digest {
		t.Errorf("mirror digest = %s, %v", got, err)
	}

	res, err = eng.Sync(ctx, entry, SyncOptions{})
	if err != nil {
		t.Fatalf("second Sync: %v", err)
	}
	if res.Records[0].Action != ActionSkippedCurrent || res.Records[0].Signed {
		t.Errorf("second sync record = %+v", res.Records[0])
	}
}

// assertArtifactDrift checks the self-healing after the mirror tag is
// overwritten with different content: sync must notice and re-assert the
// locked digest.
func assertArtifactDrift(ctx context.Context, t *testing.T, f *fixture, eng *Engine, entry spec.Entry,
	digest, target string) {
	t.Helper()
	f.pushImage(target, testNow)
	res, err := eng.Sync(ctx, entry, SyncOptions{})
	if err != nil {
		t.Fatalf("Sync after drift: %v", err)
	}
	if res.Records[0].Action != ActionPushed {
		t.Errorf("record after drift = %+v", res.Records[0])
	}
	got, err := f.eng.reg.Digest(ctx, target)
	if err != nil || got != digest {
		t.Errorf("digest after re-convergence = %s, %v (want %s)", got, err, digest)
	}
}

// TestEngineArtifactScanPolicy pins the scan.enabled three-way: false
// scans nothing, true scans unconditionally, auto consults the config
// media type — a mistake either scans garbage or silently skips a real
// image.
func TestEngineArtifactScanPolicy(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name     string
		enabled  string // scan block appended to the manifest; "" = auto
		runnable bool   // push a real image (vs a chart-shaped artifact)
		want     int    // refs the scanner must receive
	}{
		{"false scans nothing", "scan:\n  enabled: \"false\"\n", true, 0},
		{"true scans unconditionally", "scan:\n  enabled: \"true\"\n", false, 1},
		{"auto scans a runnable image", "", true, 1},
		{"auto skips a content artifact", "", false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			if tt.runnable {
				f.pushImage(f.host+"/bundles/bundle:1.0.0", testNow.AddDate(0, -1, 0))
			} else {
				f.pushChart(f.host+"/bundles", "bundle", "1.0.0",
					f.chartTgz("bundle", "1.0.0", "1.0.0", "unused:1.0.0"))
			}
			f.artifactManifest("example.test/bundles/bundle", tt.enabled)
			fake := &fakeScanner{name: "fake"}
			eng := scanEngine(f, fake)
			entry, err := eng.Entry("bundle")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := eng.Upgrade(ctx, entry, ""); err != nil {
				t.Fatalf("Upgrade: %v", err)
			}
			report, err := eng.Scan(ctx, entry)
			if err != nil {
				t.Fatalf("Scan: %v", err)
			}
			if len(fake.scanned) != tt.want {
				t.Errorf("scanned = %v, want %d ref(s)", fake.scanned, tt.want)
			}
			if tt.want > 0 && !strings.Contains(fake.scanned[0], "@sha256:") {
				t.Errorf("scan ref %q not digest-pinned", fake.scanned[0])
			}
			if len(report.Scanned) != tt.want {
				t.Errorf("report.Scanned = %v", report.Scanned)
			}
		})
	}
}

// artifactScanFixture upgrades a "bundle" artifact (a runnable image, or a
// chart-shaped content artifact) under the given scan block and returns an
// engine whose scanner reports findings.
func artifactScanFixture(t *testing.T, runnable bool, scanBlock string,
	findings ...scan.Finding) (*fixture, *Engine, spec.Entry, *fakeScanner) {
	t.Helper()
	f := newFixture(t)
	if runnable {
		f.pushImage(f.host+"/bundles/bundle:1.0.0", testNow.AddDate(0, -1, 0))
	} else {
		f.pushChart(f.host+"/bundles", "bundle", "1.0.0", f.chartTgz("bundle", "1.0.0", "1.0.0", "unused:1.0.0"))
	}
	f.artifactManifest("example.test/bundles/bundle", scanBlock)
	fake := &fakeScanner{name: "fake", findings: findings}
	eng := scanEngine(f, fake)
	entry, err := eng.Entry("bundle")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Upgrade(context.Background(), entry, ""); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	return f, eng, entry, fake
}

// TestEngineArtifactScanOverrides pins the unified scan block on
// artifacts: failOn and ignoreUnfixed now override the config per entry,
// as they always did for charts.
func TestEngineArtifactScanOverrides(t *testing.T) {
	high := scan.Finding{ID: "CVE-HIGH", Severity: "HIGH", Package: "p", Installed: "1", FixedIn: []string{"2"}}
	unfixed := scan.Finding{ID: "CVE-UNFIXED", Severity: "CRITICAL", Package: "p", Installed: "1"}
	tests := []struct {
		name, scanBlock, want string
	}{
		{"config defaults", "", "CVE-HIGH"},
		{"entry failOn and ignoreUnfixed", "scan:\n  failOn: [CRITICAL]\n  ignoreUnfixed: false\n", "CVE-UNFIXED"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, eng, entry, _ := artifactScanFixture(t, true, tt.scanBlock, high, unfixed)
			report, err := eng.Scan(context.Background(), entry)
			if err != nil {
				t.Fatalf("Scan: %v", err)
			}
			var got []string
			for _, f := range report.Blocking {
				got = append(got, f.ID)
			}
			if strings.Join(got, ",") != tt.want {
				t.Errorf("blocking = %v, want %s", got, tt.want)
			}
		})
	}

	t.Run("explicit true overrides a disabled config", func(t *testing.T) {
		f, _, entry, fake := artifactScanFixture(t, false, "scan:\n  enabled: true\n")
		f.config += "scan:\n  enabled: false\n"
		eng := scanEngine(f, fake)
		if _, err := eng.Scan(context.Background(), entry); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		if len(fake.scanned) != 1 {
			t.Errorf("scanned = %v, want the artifact despite scan.enabled: false", fake.scanned)
		}
	})
}

// TestEngineArtifactDeriveAllowlist pins allowlist derivation for
// artifacts: generated under the resolved policy for a runnable image,
// skipped (no file) for a content artifact with nothing to scan.
func TestEngineArtifactDeriveAllowlist(t *testing.T) {
	ctx := context.Background()
	block := "scan:\n  allowlist:\n    generate: true\n    newDays: 30\n    preamble: Bundle context.\n"

	t.Run("runnable image", func(t *testing.T) {
		// Upgrade derived it once already (adding the finding); a re-run
		// keeps it.
		f, eng, entry, _ := artifactScanFixture(t, true, block, criticalFinding)
		res, err := eng.DeriveAllowlist(ctx, entry)
		if err != nil {
			t.Fatalf("DeriveAllowlist: %v", err)
		}
		if !res.Generated || res.Kept != 1 {
			t.Errorf("result = %+v", res)
		}
		raw := f.read("artifacts/bundle/security/allowlist.yaml")
		for _, want := range []string{"# Bundle context.", "id: CVE-2026-1111", `expired_at: "2026-09-10"`} {
			if !strings.Contains(raw, want) {
				t.Errorf("allowlist lacks %q:\n%s", want, raw)
			}
		}
	})

	t.Run("content artifact skips", func(t *testing.T) {
		f, eng, entry, fake := artifactScanFixture(t, false, block, criticalFinding)
		res, err := eng.DeriveAllowlist(ctx, entry)
		if err != nil {
			t.Fatalf("DeriveAllowlist: %v", err)
		}
		if res.Generated || len(fake.scanned) != 0 {
			t.Errorf("result = %+v, scanned %v; want a skip", res, fake.scanned)
		}
		if _, err := os.Stat(filepath.Join(f.root, "artifacts/bundle/security/allowlist.yaml")); err == nil {
			t.Error("a non-runnable artifact got an allowlist")
		}
	})

	t.Run("generate false is a no-op", func(t *testing.T) {
		_, eng, entry, fake := artifactScanFixture(t, true, "", criticalFinding)
		res, err := eng.DeriveAllowlist(ctx, entry)
		if err != nil || res.Generated || len(fake.scanned) != 0 {
			t.Errorf("DeriveAllowlist = %+v, %v; scanned %v", res, err, fake.scanned)
		}
	})
}
