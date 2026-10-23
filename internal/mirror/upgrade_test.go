// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package mirror

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
)

func TestDiffSnapshots(t *testing.T) {
	tests := []struct {
		name          string
		before, after map[string]string
		want          []string
	}{
		{"identical", map[string]string{"a": "1"}, map[string]string{"a": "1"}, nil},
		{"added", map[string]string{}, map[string]string{"a": "1"}, []string{"a"}},
		{"rewritten", map[string]string{"a": "1"}, map[string]string{"a": "2"}, []string{"a"}},
		// A removal is a change: an upgrade that deletes a vendored file
		// must never report "already converged".
		{"removed", map[string]string{"a": "1", "b": "2"}, map[string]string{"a": "1"}, []string{"b"}},
		{
			"mixed and sorted",
			map[string]string{"z": "1", "m": "2"},
			map[string]string{"m": "3", "a": "4"},
			[]string{"a", "m", "z"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := diffSnapshots(tt.before, tt.after)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("diffSnapshots = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestResolveImagesSortedBySource pins the lock's deterministic ordering:
// resolution fans out concurrently, and the sort is the only thing keeping
// the committed lock reproducible — which the validate gate byte-compares.
func TestResolveImagesSortedBySource(t *testing.T) {
	f := newFixture(t)
	f.pushImage(f.host+"/apps/bbb:1.0.0", testNow.AddDate(0, -1, 0))
	f.pushImage(f.host+"/apps/aaa:1.0.0", testNow.AddDate(0, -1, 0))
	eng := f.engine()

	images, err := eng.resolveImages(context.Background(), "demo",
		[]string{"example.test/apps/bbb:1.0.0", "example.test/apps/aaa:1.0.0"})
	if err != nil {
		t.Fatalf("resolveImages: %v", err)
	}
	if len(images) != 2 || images[0].Source != "example.test/apps/aaa:1.0.0" ||
		images[1].Source != "example.test/apps/bbb:1.0.0" {
		t.Errorf("lock order = %+v, want sorted by source", images)
	}
}

// trackedFixture writes a converged chart whose one image is tracked:
// 2.320.0 is pinned, 2.321.0 has soaked, 2.322.0 is inside the cooldown.
func trackedFixture(t *testing.T) (*fixture, string) {
	t.Helper()
	f := newFixture(t)
	runner := f.host + "/apps/runner"
	f.pushImage(runner+":2.320.0", testNow.AddDate(0, 0, -40))
	f.pushImage(runner+":2.321.0", testNow.AddDate(0, 0, -10))
	f.pushImage(runner+":2.322.0", testNow.AddDate(0, 0, -1))
	f.pushChart(f.host+"/upstream", "demo", "1.0.0", f.chartTgz("demo", "1.0.0", "1.0.0", runner+":0.0.0"))
	f.write("charts/demo/manifest.yaml", fmt.Sprintf(`apiVersion: mirror.patchy.bitwisemedia.uk/v1alpha1
kind: Chart
name: demo
chart:
  repo: oci://%[1]s/upstream
  name: demo
  version: "1.0.0"
  versionConstraint: ">=1.0.0 <2.0.0"
discovery:
  valuesFiles: [values/discovery.yaml]
images:
  track:
    - image: %[2]s
      versionConstraint: ">=2.320.0 <3.0.0"
      valuesPath: .image
  verifyUpstream:
    - match: "*"
      provider: none
publish:
  chartRepo: charts/demo
`, f.host, runner))
	f.write("charts/demo/values/discovery.yaml",
		"# renovate: datasource=docker depName="+runner+"\nimage: "+runner+":2.320.0\n")
	eng := f.engine()
	entry, err := eng.Entry("demo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.UpgradePinned(context.Background(), entry); err != nil {
		t.Fatalf("UpgradePinned: %v", err)
	}
	return f, runner
}

// TestUpgradePinnedKeepsTrackedPin pins the image-only bump: an external
// updater moved the tracked tag (here to one still inside the cooldown, so
// a re-pick would move it), and --pinned must relock around it without
// touching the pin or the manifest.
func TestUpgradePinnedKeepsTrackedPin(t *testing.T) {
	f, runner := trackedFixture(t)
	values := "# renovate: datasource=docker depName=" + runner + "\nimage: " + runner + ":2.322.0\n"
	f.write("charts/demo/values/discovery.yaml", values)
	eng := f.engine()
	entry, err := eng.Entry("demo")
	if err != nil {
		t.Fatal(err)
	}

	res, err := eng.UpgradePinned(context.Background(), entry)
	if err != nil {
		t.Fatalf("UpgradePinned: %v", err)
	}
	if got := f.read("charts/demo/values/discovery.yaml"); got != values {
		t.Errorf("values rewritten:\n%s\nwant:\n%s", got, values)
	}
	if res.From != "1.0.0" || res.To != "1.0.0" || len(res.Tracked) != 0 {
		t.Errorf("result = %+v", res)
	}
	if !slices.Contains(res.Changed, "images.lock.yaml") || slices.Contains(res.Changed, "manifest.yaml") {
		t.Errorf("changed = %v, want the lock and not the manifest", res.Changed)
	}
	lock, err := spec.LoadImagesLock(entry.LockPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(lock.Images) != 1 || lock.Images[0].Source != runner+":2.322.0" {
		t.Errorf("lock images = %+v", lock.Images)
	}
}

func TestUpgradePinnedRejectsBadPins(t *testing.T) {
	tests := []struct {
		name string
		edit func(f *fixture, runner string)
		want string
	}{
		{
			"chart version outside constraint",
			func(f *fixture, _ string) {
				f.write("charts/demo/manifest.yaml",
					strings.Replace(f.read("charts/demo/manifest.yaml"), `version: "1.0.0"`, `version: "2.0.0"`, 1))
			},
			`pinned version 2.0.0 does not satisfy versionConstraint ">=1.0.0 <2.0.0"`,
		},
		{
			"tracked tag outside constraint",
			func(f *fixture, runner string) {
				f.write("charts/demo/values/discovery.yaml", "image: "+runner+":3.0.0\n")
			},
			`does not satisfy versionConstraint ">=2.320.0 <3.0.0"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, runner := trackedFixture(t)
			lock := f.read("charts/demo/images.lock.yaml")
			tt.edit(f, runner)
			eng := f.engine()
			entry, err := eng.Entry("demo")
			if err != nil {
				t.Fatal(err)
			}
			_, err = eng.UpgradePinned(context.Background(), entry)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want one containing %q", err, tt.want)
			}
			if f.read("charts/demo/images.lock.yaml") != lock {
				t.Error("a rejected pin still regenerated the lock")
			}
		})
	}
}

// TestUpgradePinnedRejectsDivergedLockstep pins the half-pair guard: a
// lockstep member bumped without its partner must fail, whichever member
// the task runs for.
func TestUpgradePinnedRejectsDivergedLockstep(t *testing.T) {
	f := newFixture(t)
	img := f.host + "/apps/app"
	f.pushImage(img+":1.0.0", testNow.AddDate(0, -1, 0))
	for _, name := range []string{"a", "b"} {
		f.pushChart(f.host+"/upstream", name, "1.0.0", f.chartTgz(name, "1.0.0", "1.0.0", img+":1.0.0"))
		f.pushChart(f.host+"/upstream", name, "1.1.0", f.chartTgz(name, "1.1.0", "1.1.0", img+":1.0.0"))
		f.chartManifest(name, "  lockstep: pair\n")
		f.write("charts/"+name+"/values/discovery.yaml", "{}\n")
	}
	f.write("charts/a/manifest.yaml",
		strings.Replace(f.read("charts/a/manifest.yaml"), `version: "1.0.0"`, `version: "1.1.0"`, 1))
	eng := f.engine()

	for _, name := range []string{"a", "b"} {
		entry, err := eng.Entry(name)
		if err != nil {
			t.Fatal(err)
		}
		_, err = eng.UpgradePinned(context.Background(), entry)
		if err == nil || !strings.Contains(err.Error(), "lockstep group pair diverged") {
			t.Errorf("%s: error = %v, want a lockstep divergence", name, err)
		}
	}

	// Once the partner catches up, both converge.
	f.write("charts/b/manifest.yaml",
		strings.Replace(f.read("charts/b/manifest.yaml"), `version: "1.0.0"`, `version: "1.1.0"`, 1))
	for _, name := range []string{"a", "b"} {
		entry, err := eng.Entry(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := eng.UpgradePinned(context.Background(), entry); err != nil {
			t.Errorf("%s: UpgradePinned: %v", name, err)
		}
	}
}

// TestUpgradePinnedFollowsAppVersionExtras pins the chart-only bump: an
// external updater moves chart.version alone, and {appVersion} extras
// (cert-manager's acmesolver) must follow the newly vendored chart.
func TestUpgradePinnedFollowsAppVersionExtras(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for _, v := range []string{"1.0.0", "1.1.0"} {
		f.pushImage(f.host+"/apps/app:"+v, testNow.AddDate(0, -1, 0))
		f.pushImage(f.host+"/apps/solver:"+v, testNow.AddDate(0, -1, 0))
		f.pushChart(f.host+"/upstream", "demo", v, f.chartTgz("demo", v, v, appCanonical+":"+v))
	}
	f.chartManifest("demo", "")
	f.write("charts/demo/manifest.yaml", strings.Replace(f.read("charts/demo/manifest.yaml"), "images:\n",
		"images:\n  extra:\n    - image: \"example.test/apps/solver:{appVersion}\"\n      reason: flag-configured\n", 1))
	f.write("charts/demo/values/discovery.yaml", "{}\n")
	eng := f.engine()
	entry, err := eng.Entry("demo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.UpgradePinned(ctx, entry); err != nil {
		t.Fatalf("UpgradePinned: %v", err)
	}

	f.write("charts/demo/manifest.yaml",
		strings.Replace(f.read("charts/demo/manifest.yaml"), `version: "1.0.0"`, `version: "1.1.0"`, 1))
	if entry, err = eng.Entry("demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.UpgradePinned(ctx, entry); err != nil {
		t.Fatalf("UpgradePinned after bump: %v", err)
	}
	lock, err := spec.LoadImagesLock(entry.LockPath())
	if err != nil {
		t.Fatal(err)
	}
	sources := make([]string, 0, len(lock.Images))
	for _, img := range lock.Images {
		sources = append(sources, img.Source)
	}
	want := []string{"example.test/apps/app:1.1.0", "example.test/apps/solver:1.1.0"}
	if lock.Chart.Version != "1.1.0" || !reflect.DeepEqual(sources, want) {
		t.Errorf("lock = %s %v, want 1.1.0 %v", lock.Chart.Version, sources, want)
	}
}
