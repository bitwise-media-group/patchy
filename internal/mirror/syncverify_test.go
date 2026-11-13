// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package mirror

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
	"github.com/bitwise-media-group/patchy/internal/mirror/verify"
)

// upstreamWorld is a two-registry store whose verify seam tells the two
// kinds of signature apart: refs under the mirror registries carry a valid
// mirror signature once the fake signer signed them, while upstream refs
// verify only when the test marks upstream as signed. Upstream checks are
// recorded, so tests can count them.
type upstreamWorld struct {
	f              *fixture
	eng            *Engine
	entry          spec.Entry
	signer         *fakeSigner
	upstreamSigned bool
	upstreamCalls  []string
}

func newUpstreamWorld(t *testing.T) *upstreamWorld {
	t.Helper()
	f := newFixture(t)
	f.setRegistries(fmt.Sprintf("  - name: a\n    url: %[1]s/mirror-a\n  - name: b\n    url: %[1]s/mirror-b\n", f.host))
	f.pushImage(f.host+"/apps/app:1.0.0", testNow.AddDate(0, -1, 0))
	f.pushChart(f.host+"/upstream", "demo", "1.0.0", f.chartTgz("demo", "1.0.0", "1.0.0", appCanonical+":1.0.0"))
	f.write("charts/demo/manifest.yaml", `apiVersion: mirror.patchy.bitwisemedia.uk/v1alpha1
kind: Chart
name: demo
chart:
  repo: oci://`+f.host+`/upstream
  name: demo
  version: "1.0.0"
  verifyUpstream:
    provider: cosign-keyless
    certificateIdentityRegexp: "^https://github.com/example/"
discovery:
  valuesFiles: [values/discovery.yaml]
images:
  verifyUpstream:
    - match: "example.test/apps/*"
      provider: cosign-keyless
      certificateIdentityRegexp: "^https://github.com/example/"
publish:
  chartRepo: charts/demo
`)
	f.write("charts/demo/values/discovery.yaml", "{}\n")

	w := &upstreamWorld{f: f, signer: &fakeSigner{}}
	w.eng = New(Config{
		Root:   f.root,
		Global: f.global(),
		Now:    func() time.Time { return testNow },
		Verify: func(_ context.Context, s verify.Subject) error {
			if strings.HasPrefix(s.Ref, f.host+"/mirror-") {
				if slices.Contains(w.signer.signed, s.Ref) {
					return nil
				}
				return errors.New("no mirror signature")
			}
			w.upstreamCalls = append(w.upstreamCalls, s.Ref)
			if w.upstreamSigned {
				return nil
			}
			return errors.New("no upstream signature")
		},
		NewSigner: func(*spec.Signing) (ArtifactSigner, error) { return w.signer, nil },
	})
	f.eng = w.eng
	entry, err := w.eng.Entry("demo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.eng.Upgrade(context.Background(), entry, ""); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	w.entry = entry
	return w
}

// TestEngineSyncVerifiesUpstream pins sync's own provenance gate: before
// anything is pushed or signed, every subject's verifyUpstream rule is
// checked — once per subject however many registries it publishes to —
// and a converged store re-runs without a single upstream check.
func TestEngineSyncVerifiesUpstream(t *testing.T) {
	ctx := context.Background()

	t.Run("unsigned upstream pushes and signs nothing", func(t *testing.T) {
		w := newUpstreamWorld(t)
		_, err := w.eng.Sync(ctx, w.entry, SyncOptions{})
		if err == nil || !strings.Contains(err.Error(), "upstream signature verification failed for chart demo") {
			t.Fatalf("Sync = %v, want the chart's upstream verification to fail", err)
		}
		if _, exists, _ := w.eng.reg.Exists(ctx, w.f.host+"/mirror-a/charts/demo:1.0.0"); exists {
			t.Error("chart pushed despite the failed upstream check")
		}
		if len(w.signer.signed) != 0 {
			t.Errorf("signed %v despite the failed upstream check", w.signer.signed)
		}
	})

	t.Run("dry run plans the checks without running them", func(t *testing.T) {
		w := newUpstreamWorld(t)
		if _, err := w.eng.Sync(ctx, w.entry, SyncOptions{DryRun: true}); err != nil {
			t.Fatalf("Sync dry-run: %v", err)
		}
		if len(w.upstreamCalls) != 0 {
			t.Errorf("dry run verified upstream: %v", w.upstreamCalls)
		}
	})

	t.Run("verified once per subject, never again once converged", func(t *testing.T) {
		w := newUpstreamWorld(t)
		w.upstreamSigned = true
		res, err := w.eng.Sync(ctx, w.entry, SyncOptions{})
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}
		if len(res.Records) != 4 {
			t.Fatalf("records = %+v, want chart and image at both registries", res.Records)
		}
		// Chart and image, each once, though both publish to two registries.
		lock, err := spec.LoadImagesLock(w.entry.LockPath())
		if err != nil {
			t.Fatal(err)
		}
		want := []string{w.f.host + "/upstream/demo:1.0.0", w.f.host + "/apps/app@" + lock.Images[0].Digest}
		if !slices.Equal(w.upstreamCalls, want) {
			t.Errorf("upstream checks = %v, want %v", w.upstreamCalls, want)
		}

		w.upstreamCalls = nil
		if _, err := w.eng.Sync(ctx, w.entry, SyncOptions{}); err != nil {
			t.Fatalf("second Sync: %v", err)
		}
		if len(w.upstreamCalls) != 0 {
			t.Errorf("converged re-run verified upstream: %v", w.upstreamCalls)
		}
	})

	t.Run("an unsigned existing copy is verified before it is signed", func(t *testing.T) {
		w := newUpstreamWorld(t)
		w.upstreamSigned = true
		if _, err := w.eng.Sync(ctx, w.entry, SyncOptions{}); err != nil {
			t.Fatalf("Sync: %v", err)
		}
		// The mirror signatures vanish (say, pushed by a tool that never
		// signed): re-signing vouches for the copies again, so upstream is
		// checked again first.
		w.signer.signed = nil
		w.upstreamSigned = false
		w.upstreamCalls = nil
		if _, err := w.eng.Sync(ctx, w.entry, SyncOptions{}); err == nil {
			t.Fatal("Sync re-signed copies whose upstream no longer verifies")
		}
		if len(w.signer.signed) != 0 {
			t.Errorf("signed %v", w.signer.signed)
		}
	})
}
