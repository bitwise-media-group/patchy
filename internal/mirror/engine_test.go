// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package mirror

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/bitwise-media-group/patchy/internal/mirror/helmchart"
	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
)

// testNow anchors the injected clock.
var testNow = time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)

// fixture is one fully-populated test world: an in-memory registry and a
// mirror store on disk.
type fixture struct {
	t    *testing.T
	host string
	root string
	eng  *Engine
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	s := httptest.NewServer(registry.New(registry.WithReferrersSupport(true)))
	t.Cleanup(s.Close)
	u, err := url.Parse(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, host: u.Host, root: t.TempDir()}

	// Canonical image sources use a clean host name; the rewrite reroutes
	// pulls to the in-memory registry — the same shape as a pull-through
	// cache in production, and it keeps canonical paths port-free.
	mirrorYAML := fmt.Sprintf(`apiVersion: mirror.patchy.bitwisemedia.uk/v1alpha1
kind: MirrorConfig
registry:
  url: %s/mirror
signing:
  provider: keyless
  keyless:
    certificateIdentity: https://example.test/.github/workflows/publish.yaml@refs/heads/main
    certificateOidcIssuer: https://token.example.test
update:
  cooldownDays: 3
sourceRegistryRewrites:
  example.test: %s
`, f.host, f.host)
	f.write("mirror.yaml", mirrorYAML)
	return f
}

// appCanonical is the canonical source name of the test app image; the
// rewrite maps it back to the registry at pull time.
const appCanonical = "example.test/apps/app"

// engine (re)builds the engine so config edits take effect.
func (f *fixture) engine() *Engine {
	f.t.Helper()
	global, err := spec.LoadConfig(filepath.Join(f.root, "mirror.yaml"))
	if err != nil {
		f.t.Fatal(err)
	}
	f.eng = New(Config{Root: f.root, Global: global, Now: func() time.Time { return testNow }})
	return f.eng
}

// write puts a file under the store root.
func (f *fixture) write(rel, content string) {
	f.t.Helper()
	path := filepath.Join(f.root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// read returns a store file's content.
func (f *fixture) read(rel string) string {
	f.t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.root, rel))
	if err != nil {
		f.t.Fatal(err)
	}
	return string(raw)
}

// pushImage pushes a random image with a created timestamp, returning its
// digest.
func (f *fixture) pushImage(ref string, created time.Time) string {
	f.t.Helper()
	img, err := random.Image(128, 1)
	if err != nil {
		f.t.Fatal(err)
	}
	cfg, err := img.ConfigFile()
	if err != nil {
		f.t.Fatal(err)
	}
	cfg = cfg.DeepCopy()
	cfg.Created = v1.Time{Time: created}
	img, err = mutate.ConfigFile(img, cfg)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := crane.Push(img, ref); err != nil {
		f.t.Fatalf("push %s: %v", ref, err)
	}
	d, err := img.Digest()
	if err != nil {
		f.t.Fatal(err)
	}
	return d.String()
}

// chartTgz builds a minimal valid chart archive.
func (f *fixture) chartTgz(name, version, appVersion, defaultImage string) []byte {
	f.t.Helper()
	files := map[string]string{
		name + "/Chart.yaml": fmt.Sprintf("apiVersion: v2\nname: %s\nversion: %s\nappVersion: %s\n",
			name, version, appVersion),
		name + "/values.yaml": fmt.Sprintf("image: %s\n", defaultImage),
		name + "/templates/deploy.yaml": `apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .Release.Name }}
  namespace: {{ .Release.Namespace }}
spec:
  template:
    spec:
      containers:
        - name: app
          image: {{ .Values.image }}
`,
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for fname, content := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: fname, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg,
		}); err != nil {
			f.t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			f.t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		f.t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		f.t.Fatal(err)
	}
	return buf.Bytes()
}

// pushChart publishes a chart tgz as an OCI chart artifact.
func (f *fixture) pushChart(repoPath, name, version string, tgz []byte) {
	f.t.Helper()
	layer := static.NewLayer(tgz, types.MediaType(helmchart.ChartContentLayerMediaType))
	img := mutate.MediaType(empty.Image, types.OCIManifestSchema1)
	img = mutate.ConfigMediaType(img, types.MediaType(helmchart.ChartConfigMediaType))
	img, err := mutate.AppendLayers(img, layer)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := crane.Push(img, fmt.Sprintf("%s/%s:%s", repoPath, name, version)); err != nil {
		f.t.Fatal(err)
	}
}

// chartManifest writes a chart entry pinned at 1.0.0 with a stay-in-major
// constraint.
func (f *fixture) chartManifest(name, extraBlocks string) {
	f.write(filepath.Join("charts", name, "manifest.yaml"), fmt.Sprintf(`apiVersion: mirror.patchy.bitwisemedia.uk/v1alpha1
kind: Chart
name: %[1]s
chart:
  repo: oci://%[2]s/upstream
  name: %[1]s
  version: "1.0.0"
  versionConstraint: ">=1.0.0 <2.0.0"
%[3]sdiscovery:
  namespace: %[1]s-system
  valuesFiles: [values/discovery.yaml]
images:
  verifyUpstream:
    - match: "*"
      provider: none
publish:
  chartRepo: charts/%[1]s
`, name, f.host, extraBlocks))
}

func TestEngineChartLifecycle(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	app := appCanonical
	appDigest := f.pushImage(f.host+"/apps/app:1.0.0", testNow.AddDate(0, -1, 0))

	// Upstream publishes 1.0.0 and 1.1.0 of the demo chart.
	f.pushChart(f.host+"/upstream", "demo", "1.0.0", f.chartTgz("demo", "1.0.0", "1.0.0", app+":1.0.0"))
	f.pushChart(f.host+"/upstream", "demo", "1.1.0", f.chartTgz("demo", "1.1.0", "1.1.0", app+":1.1.0"))
	f.pushImage(f.host+"/apps/app:1.1.0", testNow.AddDate(0, 0, -10))

	f.chartManifest("demo", "")
	f.write("charts/demo/values/discovery.yaml", "# discovery values\n{}\n")
	eng := f.engine()

	entry, err := eng.Entry("demo")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("regenerate", func(t *testing.T) {
		assertLifecycleRegenerate(ctx, t, f, eng, entry, app, appDigest)
	})
	t.Run("check reports update", func(t *testing.T) {
		assertLifecycleCheck(ctx, t, eng, entry)
	})
	t.Run("upgrade converges and is idempotent", func(t *testing.T) {
		assertLifecycleUpgrade(ctx, t, f, eng, entry, app)
	})
}

// assertLifecycleRegenerate checks the vendor/render/discover pass.
func assertLifecycleRegenerate(ctx context.Context, t *testing.T, f *fixture, eng *Engine,
	entry spec.Entry, app, appDigest string) {
	t.Helper()
	facts, err := eng.Regenerate(ctx, entry, entry.Dir, true)
	if err != nil {
		t.Fatalf("Regenerate: %v", err)
	}
	if err := WriteFacts(entry, facts); err != nil {
		t.Fatal(err)
	}
	lock, err := spec.LoadImagesLock(entry.LockPath())
	if err != nil {
		t.Fatal(err)
	}
	if lock.Chart.Name != "demo" || lock.Chart.Version != "1.0.0" || lock.Chart.UpstreamTgzSha256 == "" {
		t.Errorf("lock chart = %+v", lock.Chart)
	}
	if len(lock.Images) != 1 || lock.Images[0].Source != app+":1.0.0" || lock.Images[0].Digest != appDigest {
		t.Errorf("lock images = %+v", lock.Images)
	}
	wantTarget := f.host + "/mirror/images/" + app + ":1.0.0"
	if lock.Images[0].Target != wantTarget {
		t.Errorf("target = %q, want %q", lock.Images[0].Target, wantTarget)
	}
	if !strings.Contains(f.read("charts/demo/rendered/manifests.yaml"), "image: "+app+":1.0.0") {
		t.Error("rendered output missing the image")
	}
	if _, err := os.Stat(filepath.Join(entry.Dir, "vendor", "demo", "Chart.yaml")); err != nil {
		t.Errorf("vendor tree: %v", err)
	}
}

// assertLifecycleCheck checks the update plan against the newer upstream.
func assertLifecycleCheck(ctx context.Context, t *testing.T, eng *Engine, entry spec.Entry) {
	t.Helper()
	plan, err := eng.CheckUpdates(ctx, []spec.Entry{entry})
	if err != nil {
		t.Fatalf("CheckUpdates: %v", err)
	}
	if len(plan.Groups) != 1 {
		t.Fatalf("groups = %+v", plan.Groups)
	}
	g := plan.Groups[0]
	if g.Group != "demo" || g.Target != "1.1.0" || g.Members[0].Current != "1.0.0" {
		t.Errorf("group = %+v", g)
	}
}

// assertLifecycleUpgrade checks convergence and the clean no-op re-run.
func assertLifecycleUpgrade(ctx context.Context, t *testing.T, f *fixture, eng *Engine,
	entry spec.Entry, app string) {
	t.Helper()
	res, err := eng.Upgrade(ctx, entry, "1.1.0")
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if res.From != "1.0.0" || res.To != "1.1.0" || len(res.Changed) == 0 {
		t.Errorf("result = %+v", res)
	}
	if !strings.Contains(f.read("charts/demo/manifest.yaml"), `version: "1.1.0"`) {
		t.Error("manifest pin not spliced")
	}
	lock, err := spec.LoadImagesLock(entry.LockPath())
	if err != nil {
		t.Fatal(err)
	}
	if lock.Chart.Version != "1.1.0" || lock.Images[0].Source != app+":1.1.0" {
		t.Errorf("lock after upgrade = %+v", lock)
	}

	// Second run: fully converged, clean no-op.
	entry, err = eng.Entry("demo")
	if err != nil {
		t.Fatal(err)
	}
	res, err = eng.Upgrade(ctx, entry, "")
	if err != nil {
		t.Fatalf("second Upgrade: %v", err)
	}
	if len(res.Changed) != 0 {
		t.Errorf("second upgrade changed %v", res.Changed)
	}

	// And the plan is now empty.
	plan, err := eng.CheckUpdates(ctx, []spec.Entry{entry})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Groups) != 0 {
		t.Errorf("plan after convergence = %+v", plan.Groups)
	}
}

func TestEngineLockstepTarget(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	img := f.host + "/apps/app"
	f.pushImage(img+":1.0.0", testNow.AddDate(0, -1, 0))

	// Two lockstep charts: "a" has 1.0.0 and 1.1.0 upstream, "b" only
	// 1.0.5 — the group target is min(1.1.0, 1.0.5)... but member "a"
	// pinned at 1.0.0 must still move to 1.0.5? No: b's newest is 1.0.5,
	// a's newest is 1.1.0, so the group holds at 1.0.5.
	f.pushChart(f.host+"/upstream", "a", "1.0.0", f.chartTgz("a", "1.0.0", "1.0.0", img+":1.0.0"))
	f.pushChart(f.host+"/upstream", "a", "1.1.0", f.chartTgz("a", "1.1.0", "1.1.0", img+":1.0.0"))
	f.pushChart(f.host+"/upstream", "b", "1.0.5", f.chartTgz("b", "1.0.5", "1.0.5", img+":1.0.0"))

	f.chartManifest("a", "  lockstep: pair\n")
	f.chartManifest("b", "  lockstep: pair\n")
	f.write("charts/a/values/discovery.yaml", "{}\n")
	f.write("charts/b/values/discovery.yaml", "{}\n")
	eng := f.engine()

	entries, err := eng.Entries()
	if err != nil {
		t.Fatal(err)
	}
	plan, err := eng.CheckUpdates(ctx, entries)
	if err != nil {
		t.Fatalf("CheckUpdates: %v", err)
	}
	if len(plan.Groups) != 1 {
		t.Fatalf("groups = %+v", plan.Groups)
	}
	g := plan.Groups[0]
	if g.Group != "pair" || g.Target != "1.0.5" || len(g.Members) != 2 {
		t.Errorf("group = %+v", g)
	}
	for _, m := range g.Members {
		if m.Target != "1.0.5" {
			t.Errorf("member %s target = %s", m.Name, m.Target)
		}
	}
}

func TestEngineTrackedPick(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	runner := f.host + "/apps/runner"
	// 2.322.0 is fresh (inside the 3-day cooldown), 2.321.0 has soaked.
	f.pushImage(runner+":2.320.0", testNow.AddDate(0, 0, -40))
	f.pushImage(runner+":2.321.0", testNow.AddDate(0, 0, -10))
	f.pushImage(runner+":2.322.0", testNow.AddDate(0, 0, -1))

	f.pushChart(f.host+"/upstream", "demo", "1.0.0", f.chartTgz("demo", "1.0.0", "1.0.0", runner+":0.0.0"))
	// chartManifest's shape doesn't cover track rules; write the manifest
	// fully by hand.
	f.write("charts/demo/manifest.yaml", fmt.Sprintf(`apiVersion: mirror.patchy.bitwisemedia.uk/v1alpha1
kind: Chart
name: demo
chart:
  repo: oci://%[1]s/upstream
  name: demo
  version: "1.0.0"
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
	f.write("charts/demo/values/discovery.yaml", "# tracked pin — never hand-edit\nimage: "+runner+":2.320.0\n")
	eng := f.engine()

	entry, err := eng.Entry("demo")
	if err != nil {
		t.Fatal(err)
	}
	plans, err := eng.ApplyTracks(ctx, entry)
	if err != nil {
		t.Fatalf("ApplyTracks: %v", err)
	}
	if len(plans) != 1 || plans[0].Current != "2.320.0" || plans[0].Selected != "2.321.0" {
		t.Errorf("plans = %+v", plans)
	}
	values := f.read("charts/demo/values/discovery.yaml")
	want := "# tracked pin — never hand-edit\nimage: " + runner + ":2.321.0\n"
	if values != want {
		t.Errorf("values after pick:\n%s\nwant:\n%s", values, want)
	}
}

func TestEngineTrackedPickBareTag(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	app := f.host + "/apps/app"
	// v3.0.0 sits outside the derived constraint (the v2 train of the
	// pin), v2.45.1 has soaked, v2.45.2 is fresh.
	f.pushImage(app+":v2.44.0", testNow.AddDate(0, 0, -40))
	f.pushImage(app+":v2.45.1", testNow.AddDate(0, 0, -10))
	f.pushImage(app+":v2.45.2", testNow.AddDate(0, 0, -1))
	f.pushImage(app+":v3.0.0", testNow.AddDate(0, 0, -20))

	f.pushChart(f.host+"/upstream", "demo", "1.0.0", f.chartTgz("demo", "1.0.0", "1.0.0", app+":0.0.0"))
	// No versionConstraint: the rule derives the pin's release train. The
	// values pin is a bare tag (the chart splits repository from tag).
	f.write("charts/demo/manifest.yaml", fmt.Sprintf(`apiVersion: mirror.patchy.bitwisemedia.uk/v1alpha1
kind: Chart
name: demo
chart:
  repo: oci://%[1]s/upstream
  name: demo
  version: "1.0.0"
discovery:
  valuesFiles: [values/discovery.yaml]
images:
  track:
    - image: %[2]s
      valuesPath: .image.tag
  verifyUpstream:
    - match: "*"
      provider: none
publish:
  chartRepo: charts/demo
`, f.host, app))
	f.write("charts/demo/values/discovery.yaml", "image:\n  # tracked pin — never hand-edit\n  tag: v2.44.0\n")
	eng := f.engine()

	entry, err := eng.Entry("demo")
	if err != nil {
		t.Fatal(err)
	}
	plans, err := eng.ApplyTracks(ctx, entry)
	if err != nil {
		t.Fatalf("ApplyTracks: %v", err)
	}
	if len(plans) != 1 || plans[0].Current != "v2.44.0" || plans[0].Selected != "v2.45.1" || !plans[0].TagOnly {
		t.Errorf("plans = %+v", plans)
	}
	values := f.read("charts/demo/values/discovery.yaml")
	want := "image:\n  # tracked pin — never hand-edit\n  tag: v2.45.1\n"
	if values != want {
		t.Errorf("values after pick:\n%s\nwant:\n%s", values, want)
	}
}
