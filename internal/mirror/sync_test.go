// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package mirror

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bitwise-media-group/patchy/internal/mirror/scan"
	"github.com/bitwise-media-group/patchy/internal/mirror/sign"
	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
	"github.com/bitwise-media-group/patchy/internal/mirror/verify"
)

// keyPair generates a cosign keypair on disk for offline sign/verify,
// shelling out to the same binary sign/verify now exec — no committed
// private-key fixture.
func keyPair(t *testing.T) (keyPath, pubPath string, password []byte) {
	t.Helper()
	if _, err := exec.LookPath("cosign"); err != nil {
		t.Skipf("cosign binary not on PATH (mise install): %v", err)
	}
	password = []byte("test-password")
	dir := t.TempDir()
	cmd := exec.Command("cosign", "generate-key-pair")
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(), "COSIGN_PASSWORD="+string(password))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cosign generate-key-pair: %v (%s)", err, out)
	}
	return filepath.Join(dir, "cosign.key"), filepath.Join(dir, "cosign.pub"), password
}

// keyedEngine rebuilds the fixture's engine with key-based sign/verify
// seams — the same real cosign code paths, minus the keyless trust
// services.
func keyedEngine(f *fixture, keyPath, pubPath string, password []byte) *Engine {
	f.t.Helper()
	global, err := spec.LoadConfig(filepath.Join(f.root, "mirror.yaml"))
	if err != nil {
		f.t.Fatal(err)
	}
	eng := New(Config{
		Root:   f.root,
		Global: global,
		Now:    func() time.Time { return testNow },
		Verify: func(ctx context.Context, s verify.Subject) error {
			return verify.Verify(ctx, scan.ExecRunner{}, verify.Subject{
				Ref:               s.Ref,
				KeyRef:            pubPath,
				IgnoreTlog:        true,
				AllowHTTPRegistry: true,
			})
		},
		NewSigner: func(*spec.Signing) (ArtifactSigner, error) {
			return sign.NewWithKeyFile(keyPath, password, scan.ExecRunner{}), nil
		},
	})
	f.eng = eng
	return eng
}

func TestEngineSync(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	keyPath, pubPath, password := keyPair(t)

	app := appCanonical
	f.pushImage(f.host+"/apps/app:1.0.0", testNow.AddDate(0, -1, 0))
	f.pushChart(f.host+"/upstream", "demo", "1.0.0", f.chartTgz("demo", "1.0.0", "1.0.0", app+":1.0.0"))
	f.chartManifest("demo", "")
	f.write("charts/demo/values/discovery.yaml", "{}\n")

	eng := keyedEngine(f, keyPath, pubPath, password)
	entry, err := eng.Entry("demo")
	if err != nil {
		t.Fatal(err)
	}
	// Converge the tree first (vendor/render/lock).
	if _, err := eng.Upgrade(ctx, entry, ""); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}

	t.Run("dry run plans the work", func(t *testing.T) {
		res, err := eng.Sync(ctx, entry, true)
		if err != nil {
			t.Fatalf("Sync dry-run: %v", err)
		}
		if len(res.Records) != 2 {
			t.Fatalf("records = %+v", res.Records)
		}
		for _, r := range res.Records {
			if r.Action != ActionPushed {
				t.Errorf("dry-run action for %s = %s", r.Ref, r.Action)
			}
		}
		// Dry run must not have published anything.
		if _, exists, _ := f.eng.reg.Exists(ctx, f.host+"/mirror/charts/demo:1.0.0"); exists {
			t.Error("dry run pushed the chart")
		}
	})

	t.Run("first sync publishes and signs", func(t *testing.T) {
		res, err := eng.Sync(ctx, entry, false)
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}
		assertFirstSync(ctx, t, f, res, pubPath)
	})

	t.Run("second sync is a clean skip", func(t *testing.T) {
		res, err := eng.Sync(ctx, entry, false)
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}
		if res.Records[0].Action != ActionSkippedTagExists || res.Records[0].Signed {
			t.Errorf("chart record = %+v", res.Records[0])
		}
		if res.Records[1].Action != ActionSkippedCurrent || res.Records[1].Signed {
			t.Errorf("image record = %+v", res.Records[1])
		}
	})

	t.Run("upstream mutation trips the wire", func(t *testing.T) {
		// Upstream re-releases 1.0.0 with different bytes.
		f.pushChart(f.host+"/upstream", "demo", "1.0.0",
			f.chartTgz("demo", "1.0.0", "mutated", app+":1.0.0"))
		_, err := eng.Sync(ctx, entry, false)
		if err == nil || !strings.Contains(err.Error(), "digest mismatch") {
			t.Errorf("want tripwire error, got %v", err)
		}
	})
}

// TestEngineSyncGuarantees pins the two sync behaviors the first-publish
// path never reaches: the committed-vendor-tree tripwire (distinct from
// the upstream tgz digest check — it catches the COMMITTED copy being
// tampered with while upstream is unchanged) and the dry run staying
// read-only over an already-published tag.
func TestEngineSyncGuarantees(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	keyPath, pubPath, password := keyPair(t)

	app := appCanonical
	f.pushImage(f.host+"/apps/app:1.0.0", testNow.AddDate(0, -1, 0))
	f.pushChart(f.host+"/upstream", "demo", "1.0.0", f.chartTgz("demo", "1.0.0", "1.0.0", app+":1.0.0"))
	f.chartManifest("demo", "")
	f.write("charts/demo/values/discovery.yaml", "{}\n")

	eng := keyedEngine(f, keyPath, pubPath, password)
	entry, err := eng.Entry("demo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Upgrade(ctx, entry, ""); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if _, err := eng.Sync(ctx, entry, false); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	t.Run("dry run over a published tag stays read-only", func(t *testing.T) {
		res, err := eng.Sync(ctx, entry, true)
		if err != nil {
			t.Fatalf("Sync dry-run: %v", err)
		}
		for _, r := range res.Records {
			if r.Action == ActionPushed || r.Signed {
				t.Errorf("dry run after publish acted: %+v", r)
			}
		}
	})

	t.Run("tampered vendor tree trips the wire", func(t *testing.T) {
		// Upstream still serves the locked bytes; the committed vendor
		// copy — what the PR review actually approved — was edited.
		f.write("charts/demo/vendor/demo/values.yaml", "image: attacker.example.test/app:1.0.0\n")
		_, err := eng.Sync(ctx, entry, false)
		if err == nil || !strings.Contains(err.Error(), "vendor tree differs") {
			t.Errorf("want vendor tripwire error, got %v", err)
		}
	})
}

// TestEngineSyncSigningOverride pins the per-entry signing seam: an entry
// carrying its own signing block must be signed (and self-verified) with
// that identity, never the global default.
func TestEngineSyncSigningOverride(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	keyPath, pubPath, password := keyPair(t)

	f.pushImage(f.host+"/bundles/bundle:1.0.0", testNow.AddDate(0, -1, 0))
	f.artifactManifest("bundle", "example.test/bundles/bundle", `signing:
  provider: kms
  kms:
    key: awskms://alias/entry-override
`)

	var resolved []*spec.Signing
	global, err := spec.LoadConfig(filepath.Join(f.root, "mirror.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	eng := New(Config{
		Root:   f.root,
		Global: global,
		Now:    func() time.Time { return testNow },
		Verify: func(ctx context.Context, s verify.Subject) error {
			return verify.Verify(ctx, scan.ExecRunner{}, verify.Subject{
				Ref: s.Ref, KeyRef: pubPath, IgnoreTlog: true, AllowHTTPRegistry: true,
			})
		},
		NewSigner: func(s *spec.Signing) (ArtifactSigner, error) {
			resolved = append(resolved, s)
			return sign.NewWithKeyFile(keyPath, password, scan.ExecRunner{}), nil
		},
	})
	f.eng = eng
	entry, err := eng.Entry("bundle")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Upgrade(ctx, entry, ""); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if _, err := eng.Sync(ctx, entry, false); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(resolved) == 0 {
		t.Fatal("NewSigner never called")
	}
	for _, s := range resolved {
		if s.Provider != "kms" || s.KMS == nil || s.KMS.Key != "awskms://alias/entry-override" {
			t.Errorf("signer built with %+v, want the entry override", s)
		}
	}
}

// assertFirstSync checks the records and the real signature a first sync
// produces.
func assertFirstSync(ctx context.Context, t *testing.T, f *fixture, res *SyncResult, pubPath string) {
	t.Helper()
	if len(res.Records) != 2 {
		t.Fatalf("records = %+v", res.Records)
	}
	chart, image := res.Records[0], res.Records[1]
	if chart.Action != ActionPushed || !chart.Signed || chart.Digest == "" {
		t.Errorf("chart record = %+v", chart)
	}
	if image.Action != ActionPushed || !image.Signed {
		t.Errorf("image record = %+v", image)
	}
	if !strings.HasPrefix(image.Ref, f.host+"/mirror/images/") {
		t.Errorf("image target = %s", image.Ref)
	}
	// The mirrored image must resolve to the locked digest, and its
	// signature must verify with the real verifier.
	got, err := f.eng.reg.Digest(ctx, image.Ref)
	if err != nil || got != image.Digest {
		t.Errorf("mirror digest = %s, %v", got, err)
	}
	err = verify.Verify(ctx, scan.ExecRunner{}, verify.Subject{
		Ref:               repoOf(image.Ref) + "@" + image.Digest,
		KeyRef:            pubPath,
		IgnoreTlog:        true,
		AllowHTTPRegistry: true,
	})
	if err != nil {
		t.Errorf("mirror signature does not verify: %v", err)
	}
}

func TestEngineVerifyUpstream(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	keyPath, pubPath, password := keyPair(t)

	app := appCanonical
	f.pushImage(f.host+"/apps/app:1.0.0", testNow.AddDate(0, -1, 0))
	f.pushChart(f.host+"/upstream", "demo", "1.0.0", f.chartTgz("demo", "1.0.0", "1.0.0", app+":1.0.0"))

	// The manifest declares key verification for the app image and a
	// documented gap for the chart.
	f.write("charts/demo/manifest.yaml", `apiVersion: mirror.patchy.bitwisemedia.uk/v1alpha1
kind: Chart
name: demo
chart:
  repo: oci://`+f.host+`/upstream
  name: demo
  version: "1.0.0"
  verifyUpstream:
    provider: none
discovery:
  valuesFiles: [values/discovery.yaml]
images:
  verifyUpstream:
    - match: "example.test/apps/*"
      provider: cosign-key
      key: `+pubPath+`
    - match: "*"
      provider: none
publish:
  chartRepo: charts/demo
`)
	f.write("charts/demo/values/discovery.yaml", "{}\n")
	eng := keyedEngine(f, keyPath, pubPath, password)

	entry, err := eng.Entry("demo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Upgrade(ctx, entry, ""); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}

	t.Run("unsigned upstream image fails", func(t *testing.T) {
		if _, err := eng.VerifyUpstream(ctx, entry); err == nil {
			t.Error("want verification failure for unsigned upstream image")
		}
	})

	t.Run("signed upstream image verifies", func(t *testing.T) {
		lock, err := spec.LoadImagesLock(entry.LockPath())
		if err != nil {
			t.Fatal(err)
		}
		signer := sign.NewWithKeyFile(keyPath, password, scan.ExecRunner{})
		// The signature lands where pulls actually happen (the rewrite
		// target), exactly where verification will look.
		if err := signer.Sign(ctx, f.host+"/apps/app@"+lock.Images[0].Digest, false); err != nil {
			t.Fatalf("sign upstream: %v", err)
		}
		report, err := eng.VerifyUpstream(ctx, entry)
		if err != nil {
			t.Fatalf("VerifyUpstream: %v", err)
		}
		if len(report.Verified) != 1 || len(report.Gaps) != 1 {
			t.Errorf("report = %+v", report)
		}
		if !strings.Contains(report.Gaps[0], "chart demo") {
			t.Errorf("gap = %q", report.Gaps[0])
		}
	})
}
