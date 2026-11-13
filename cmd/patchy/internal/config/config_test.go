// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bitwise-media-group/patchy/internal/build"
)

// roots is one isolated search world: system dir, home and workdir under a
// single temp directory.
type roots struct {
	t                     *testing.T
	system, home, workdir string
}

func newRoots(t *testing.T) *roots {
	t.Helper()
	base := t.TempDir()
	r := &roots{
		t:       t,
		system:  filepath.Join(base, "etc", "patchy"),
		home:    filepath.Join(base, "home"),
		workdir: filepath.Join(base, "work"),
	}
	for _, d := range []string{r.system, r.home, r.workdir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func (r *roots) opts() Options {
	return Options{Workdir: r.workdir, Home: r.home, SystemDir: r.system}
}

// write puts content at path (absolute or relative to the workdir).
func (r *roots) write(path, content string) {
	r.t.Helper()
	if !filepath.IsAbs(path) {
		path = filepath.Join(r.workdir, path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *roots) load() *Loaded {
	r.t.Helper()
	l, err := Load(r.opts())
	if err != nil {
		r.t.Fatalf("Load: %v", err)
	}
	return l
}

// TestLoadPrecedence writes one layer per location, each claiming dev.name
// and setting one key of its own: the most preferred layer wins the shared
// key, and every layer's own key survives the merge.
func TestLoadPrecedence(t *testing.T) {
	r := newRoots(t)
	layers := []struct{ path, id string }{
		{filepath.Join(r.system, "config.yaml"), "system"},
		{filepath.Join(r.home, ".config", "patchy", "config.yaml"), "home"},
		{"mise/patchy.yaml", "mise"},
		{"patchy/config.yaml", "patchy-dir"},
		{".mise/patchy.yaml", "dot-mise"},
		{".patchy/config.yaml", "dot-patchy"},
		{"patchy.yaml", "root"},
	}
	for i, l := range layers {
		r.write(l.path, "dev:\n  name: "+l.id+"\nmirror:\n  sourceRegistryRewrites:\n    "+
			l.id+".example: mirror.example/"+l.id+"\n")
		loaded := r.load()
		if got := loaded.File.Dev.Name; got != l.id {
			t.Errorf("with %d layer(s), dev.name = %q, want the most preferred %q", i+1, got, l.id)
		}
		if len(loaded.Sources) != i+1 {
			t.Errorf("sources = %v, want %d", loaded.Sources, i+1)
		}
	}
	loaded := r.load()
	if n := len(loaded.File.Mirror.SourceRegistryRewrites); n != len(layers) {
		t.Errorf("merged rewrites = %v, want one per layer", loaded.File.Mirror.SourceRegistryRewrites)
	}
	want := []string{
		filepath.Join(r.system, "config.yaml"),
		filepath.Join("~", ".config", "patchy", "config.yaml"),
		filepath.Join("mise", "patchy.yaml"),
		filepath.Join("patchy", "config.yaml"),
		filepath.Join(".mise", "patchy.yaml"),
		filepath.Join(".patchy", "config.yaml"),
		"patchy.yaml",
	}
	if !reflect.DeepEqual(loaded.Sources, want) {
		t.Errorf("sources =\n%v\nwant\n%v", loaded.Sources, want)
	}
}

func TestLoadFormats(t *testing.T) {
	tests := []struct{ file, content string }{
		{"patchy.yaml", "mirror:\n  workers: 7\n  directory: store\ndev:\n  enhance-timeout: 5s\n"},
		{"patchy.yml", "mirror:\n  workers: 7\n  directory: store\ndev:\n  enhance-timeout: 5s\n"},
		{"patchy.json", `{"mirror": {"workers": 7, "directory": "store"}, "dev": {"enhance-timeout": "5s"}}`},
		{"patchy.toml", "[mirror]\nworkers = 7\ndirectory = \"store\"\n[dev]\nenhance-timeout = \"5s\"\n"},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			r := newRoots(t)
			r.write(tt.file, tt.content)
			f := r.load().File
			if f.Mirror.Workers != 7 || f.Mirror.Directory != "store" || f.Dev.EnhanceTimeout != 5*time.Second {
				t.Errorf("decoded = %+v / %+v", f.Mirror, f.Dev)
			}
		})
	}
}

func TestLoadNoFiles(t *testing.T) {
	l := newRoots(t).load()
	if len(l.Sources) != 0 || l.File == nil {
		t.Errorf("empty world = %+v", l)
	}
	if _, err := l.File.MirrorConfig(); err == nil || !strings.Contains(err.Error(), "at least one registry") {
		t.Errorf("MirrorConfig without registries = %v", err)
	}
}

func TestLoadAmbiguousLocation(t *testing.T) {
	r := newRoots(t)
	r.write(".patchy/config.yaml", "{}")
	r.write(".patchy/config.toml", "")
	_, err := Load(r.opts())
	if !errors.Is(err, ErrAmbiguous) || !strings.Contains(err.Error(), "config.toml") {
		t.Fatalf("error = %v, want the ambiguity refusal naming both files", err)
	}
	// One file per location across several locations is not ambiguous.
	r2 := newRoots(t)
	r2.write("patchy.yaml", "{}")
	r2.write(".patchy/config.toml", "")
	r2.load()
}

// TestLoadSameFileOnce points two locations at one file: home's
// ~/.config/patchy is the workdir's patchy/ directory.
func TestLoadSameFileOnce(t *testing.T) {
	r := newRoots(t)
	r.home = filepath.Join(filepath.Dir(r.workdir), "h")
	if err := os.MkdirAll(filepath.Join(r.home, ".config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(r.workdir, "patchy"), filepath.Join(r.home, ".config", "patchy")); err != nil {
		t.Fatal(err)
	}
	r.write("patchy/config.yaml", "dev:\n  name: shared\n")
	l := r.load()
	if len(l.Sources) != 1 || l.Sources[0] != filepath.Join("patchy", "config.yaml") {
		t.Errorf("sources = %v, want the file once at its project position", l.Sources)
	}
}

func TestMergeSemantics(t *testing.T) {
	r := newRoots(t)
	r.write(filepath.Join(r.home, ".config", "patchy", "config.yaml"), `registries:
  - name: home
    url: home.example/x
signing:
  provider: kms
  kms: {key: awskms://alias/home}
scan:
  failOn: [CRITICAL, HIGH, MEDIUM]
  scanners:
    osv: {enabled: true}
    grype: {enabled: true}
mirror:
  sourceRegistryRewrites:
    docker.io: cache.example/dockerhub
`)
	r.write("patchy.yaml", `registries:
  - name: project
    url: project.example/x
scan:
  failOn: [CRITICAL]
  scanners:
    grype: {enabled: false}
signing: null
mirror:
  sourceRegistryRewrites:
    docker.io: null
`)
	f := r.load().File
	// Lists replace wholesale.
	if len(f.Registries) != 1 || f.Registries[0].Name != "project" {
		t.Errorf("registries = %+v, want the project list alone", f.Registries)
	}
	if !reflect.DeepEqual(f.Scan.FailOn, []string{"CRITICAL"}) {
		t.Errorf("failOn = %v", f.Scan.FailOn)
	}
	// Maps merge recursively.
	if !f.Scan.Scanners.OSVEnabled() || f.Scan.Scanners.GrypeEnabled() {
		t.Errorf("scanners = osv %v grype %v, want home's osv kept and grype overridden",
			f.Scan.Scanners.OSVEnabled(), f.Scan.Scanners.GrypeEnabled())
	}
	// null deletes an inherited key.
	if f.Signing.Provider != "" || f.Signing.KMS != nil {
		t.Errorf("signing = %+v, want deleted", f.Signing)
	}
	if len(f.Mirror.SourceRegistryRewrites) != 0 {
		t.Errorf("rewrites = %v, want docker.io deleted", f.Mirror.SourceRegistryRewrites)
	}
}

func TestMergeLeavesInputsAlone(t *testing.T) {
	base := map[string]any{"a": map[string]any{"x": 1, "y": 2}}
	overlay := map[string]any{"a": map[string]any{"y": nil, "z": 3}, "b": nil}
	got := Merge(base, overlay)
	want := map[string]any{"a": map[string]any{"x": 1, "z": 3}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Merge = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(base, map[string]any{"a": map[string]any{"x": 1, "y": 2}}) {
		t.Errorf("base mutated: %v", base)
	}
}

func TestLoadStrictKeys(t *testing.T) {
	tests := []struct {
		name, file, content string
		want                []string
	}{
		{
			name: "nested unknown key names the file and path",
			file: filepath.Join("HOME", ".config", "patchy", "config.toml"),
			content: "[[build.images]]\nname = \"controller\"\nrepository = \"patchy/controller\"\n" +
				"dockerfil = \"Dockerfile\"\n",
			want: []string{filepath.Join("~", ".config", "patchy", "config.toml") + ":",
				"'build.images[0]' has invalid keys: dockerfil"},
		},
		{
			name: "unknown key inside a build secret",
			file: "patchy.yaml",
			content: "build:\n  images:\n    - name: web\n      repository: platform/web\n" +
				"      secrets:\n        - id: npm_token\n          value: hunter2\n",
			want: []string{"patchy.yaml:", "'build.images[0].secrets[0]' has invalid keys: value"},
		},
		{
			name:    "top-level unknown key",
			file:    "patchy.yaml",
			content: "bogus: true\n",
			want:    []string{"patchy.yaml:", "unknown top-level keys: bogus"},
		},
		{
			name:    "allowlistMaxDays hint",
			file:    "patchy.yaml",
			content: "scan:\n  allowlistMaxDays: 30\n  allowlistNewDays: 30\n",
			want: []string{"scan.allowlistMaxDays moved to scan.allowlist.maxDays",
				"scan.allowlistNewDays moved to scan.allowlist.newDays"},
		},
		{
			name:    "mirror.yaml leftovers",
			file:    "patchy.yaml",
			content: "apiVersion: mirror.patchy.bitwisemedia.uk/v1alpha1\nkind: MirrorConfig\nupdate: {cooldownDays: 3}\n",
			want:    []string{"update moved to mirror.update", "apiVersion is not a patchy config key"},
		},
		{
			name:    "wrong type",
			file:    "patchy.yaml",
			content: "mirror:\n  workers: many\n",
			want:    []string{"patchy.yaml:", "mirror.workers"},
		},
		{
			name:    "malformed file",
			file:    "patchy.yaml",
			content: "dev: [",
			want:    []string{"parse config patchy.yaml"},
		},
		{
			name:    "two documents",
			file:    "patchy.yaml",
			content: "dev: {}\n---\ndev: {}\n",
			want:    []string{"single YAML document"},
		},
		{
			name:    "non-mapping top level",
			file:    "patchy.json",
			content: "[1, 2]",
			want:    []string{"top level must be a mapping"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRoots(t)
			path := tt.file
			if rest, ok := strings.CutPrefix(path, "HOME"+string(filepath.Separator)); ok {
				path = filepath.Join(r.home, rest)
			}
			r.write(path, tt.content)
			_, err := Load(r.opts())
			if err == nil {
				t.Fatal("Load accepted the config")
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q lacks %q", err, want)
				}
			}
		})
	}
}

// TestLoadPreservesKeyCase pins the reason viper is not the merge engine:
// user-defined keys (build args) keep their case.
func TestLoadPreservesKeyCase(t *testing.T) {
	r := newRoots(t)
	r.write(filepath.Join(r.home, ".config", "patchy", "config.yaml"), `build:
  images:
    - name: controller
      repository: patchy/controller
      args: {GO_VERSION: "1.26", CGO_ENABLED: "0"}
`)
	r.write("patchy.yaml", `build:
  architectures: [amd64]
  tags: ["sha-${GITHUB_SHA}"]
  images:
    - name: controller
      repository: patchy/controller
      args: {GO_VERSION: "1.27"}
      scan: {enabled: true}
`)
	l := r.load()
	img := l.File.Build.Images[0]
	if !reflect.DeepEqual(img.Args, map[string]string{"GO_VERSION": "1.27"}) {
		t.Errorf("args = %v, want the project's list-replaced images with key case intact", img.Args)
	}
	if img.Scan.Enabled != "true" {
		t.Errorf("scan.enabled from a bool = %q", img.Scan.Enabled)
	}
}

func TestBuildConfig(t *testing.T) {
	r := newRoots(t)
	r.write("patchy.yaml", `registries:
  - name: ghcr
    url: ghcr.io/org
scan:
  allowlist: {maxDays: 30, newDays: 30}
build:
  architectures: [amd64, linux/arm64/v8]
  tags: ["${VERSION}"]
  images:
    - name: controller
      repository: patchy/controller
      secrets:
        - id: npm_token
          env: NPM_TOKEN
        - id: npmrc
          src: "${RUNNER_TEMP}/.npmrc"
          optional: true
  manifests:
    - name: platform
      repository: manifests/platform
      path: deploy/flux
`)
	global, s, err := r.load().File.BuildConfig()
	if err != nil {
		t.Fatalf("BuildConfig: %v", err)
	}
	if global.Signing.Provider != "keyless" || global.Registries[0].ImageNamespace != "images" {
		t.Errorf("global not resolved: %+v", global)
	}
	entries := s.Entries()
	if len(entries) != 2 || entries[0].Kind != "image" || entries[1].Kind != "manifests" {
		t.Fatalf("entries = %+v", entries)
	}
	if !reflect.DeepEqual(entries[0].Platforms, []string{"linux/amd64", "linux/arm64/v8"}) {
		t.Errorf("platforms = %v", entries[0].Platforms)
	}
	wantSecrets := []build.Secret{
		{ID: "npm_token", Env: "NPM_TOKEN"},
		{ID: "npmrc", Src: "${RUNNER_TEMP}/.npmrc", Optional: true},
	}
	if !reflect.DeepEqual(entries[0].Image.Secrets, wantSecrets) {
		t.Errorf("secrets = %+v", entries[0].Image.Secrets)
	}

	bad := newRoots(t)
	bad.write("patchy.yaml", `registries: [{name: r, url: r.example/x}]
build:
  manifests:
    - name: platform
      repository: manifests/platform
      path: deploy/flux
      scan: {allowlist: {generate: true}}
`)
	if _, _, err := bad.load().File.BuildConfig(); err == nil ||
		!strings.Contains(err.Error(), "apply to image scans only") {
		t.Errorf("BuildConfig = %v, want the manifests scan refusal", err)
	}
}

func TestLegacyFiles(t *testing.T) {
	r := newRoots(t)
	r.write(".patchy.yaml", "dev:\n  name: legacy\n")
	l := r.load()
	if l.File.Dev.Name != "" {
		t.Errorf("legacy .patchy.yaml was read: %+v", l.File.Dev)
	}
	if len(l.Warnings) != 1 || !strings.Contains(l.Warnings[0], ".patchy.yaml is no longer read") {
		t.Errorf("warnings = %v", l.Warnings)
	}

	store := t.TempDir()
	if err := CheckMirrorRoot(store); err != nil {
		t.Errorf("CheckMirrorRoot(empty) = %v", err)
	}
	if err := os.WriteFile(filepath.Join(store, "mirror.yaml"), []byte("kind: MirrorConfig\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := CheckMirrorRoot(store)
	if !errors.Is(err, ErrLegacyMirrorConfig) {
		t.Fatalf("CheckMirrorRoot = %v, want the migration error", err)
	}
	for _, want := range []string{"patchy.yaml", "under mirror:", "scan.allowlist.maxDays"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("migration error lacks %q: %v", want, err)
		}
	}
}

func TestLoadedPath(t *testing.T) {
	l := &Loaded{Workdir: "/work"}
	if got := l.Path("deploy/flux"); got != filepath.Join("/work", "deploy", "flux") {
		t.Errorf("Path(relative) = %q", got)
	}
	if got := l.Path("/abs"); got != "/abs" {
		t.Errorf("Path(absolute) = %q", got)
	}
}

// TestPolicyEnforcement: an enforced policy is evaluated as written in its
// own layer, so a more preferred layer can tighten it but neither loosen,
// downgrade nor delete it; an unenforced one merges like any other key.
func TestPolicyEnforcement(t *testing.T) {
	const toolchain = `registries: [{name: r, url: r.example/x}]
mirror:
  policy:
    enforced: true
    images:
      allow: ["registry.k8s.io/**"]
build:
  policy:
    images:
      allow: ["registry.example.com/**"]
`
	tests := []struct {
		name    string
		project string
		// mirrorDenied / buildDenied: blocking verdicts for the probes.
		mirrorDenied, buildDenied bool
		mirrorSource              string
	}{
		{name: "toolchain only", mirrorDenied: true, buildDenied: true,
			mirrorSource: ".mise/patchy.yaml, enforced"},
		{name: "loosening an enforced policy has no effect", project: `mirror:
  policy:
    enforced: false
    mode: warn
    images:
      allow: ["**"]
build:
  policy:
    images:
      allow: ["**"]
`, mirrorDenied: true, buildDenied: false, mirrorSource: ".mise/patchy.yaml, enforced"},
		{name: "null cannot delete an enforced policy", project: "mirror:\n  policy: null\nbuild:\n  policy: null\n",
			mirrorDenied: true, buildDenied: false, mirrorSource: ".mise/patchy.yaml, enforced"},
		{name: "tightening applies on top", project: `mirror:
  policy:
    images:
      deny: ["registry.k8s.io/pause"]
`, mirrorDenied: true, buildDenied: true, mirrorSource: ".mise/patchy.yaml, enforced"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRoots(t)
			r.write(".mise/patchy.yaml", toolchain)
			if tt.project != "" {
				r.write("patchy.yaml", tt.project)
			}
			f := r.load().File
			mc, err := f.MirrorConfig()
			if err != nil {
				t.Fatalf("MirrorConfig: %v", err)
			}
			bc, _, err := f.BuildPlanConfig()
			if err != nil {
				t.Fatalf("BuildPlanConfig: %v", err)
			}
			v := mc.Policy.Check("image", "ghcr.io/acme/app:v1")
			if got := v != nil && v.Blocking(); got != tt.mirrorDenied {
				t.Errorf("mirror denied = %v (%v), want %v", got, v, tt.mirrorDenied)
			}
			if v != nil && v.Source != tt.mirrorSource {
				t.Errorf("mirror violation source = %q, want %q", v.Source, tt.mirrorSource)
			}
			bv := bc.Policy.Check("image", "ghcr.io/acme/base:v1")
			if got := bv != nil && bv.Blocking(); got != tt.buildDenied {
				t.Errorf("build denied = %v (%v), want %v", got, bv, tt.buildDenied)
			}
			pv := mc.Policy.Check("image", "registry.k8s.io/pause:3.10")
			if wantDenied := tt.name == "tightening applies on top"; (pv != nil) != wantDenied {
				t.Errorf("pause verdict = %v, want denied %v", pv, wantDenied)
			}
		})
	}
}

func TestPolicyValidationNamesTheLayer(t *testing.T) {
	r := newRoots(t)
	r.write(".mise/patchy.yaml", `registries: [{name: r, url: r.example/x}]
mirror:
  policy:
    enforced: true
    images:
      allow: ["nginx"]
`)
	_, err := r.load().File.MirrorConfig()
	if err == nil || !strings.Contains(err.Error(), "policy (.mise/patchy.yaml, enforced): images.allow[0]") {
		t.Errorf("MirrorConfig = %v, want the enforced layer named", err)
	}

	bad := newRoots(t)
	bad.write("patchy.yaml", "mirror:\n  policy:\n    images:\n      permit: []\n")
	if _, err := Load(bad.opts()); err == nil || !strings.Contains(err.Error(), "permit") {
		t.Errorf("Load = %v, want the unknown key rejected", err)
	}
}
