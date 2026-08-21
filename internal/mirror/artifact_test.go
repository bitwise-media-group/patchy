// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package mirror

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

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
