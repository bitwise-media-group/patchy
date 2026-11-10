// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package build

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"

	"github.com/bitwise-media-group/patchy/internal/build/buildx"
	"github.com/bitwise-media-group/patchy/internal/build/fluxartifact"
	"github.com/bitwise-media-group/patchy/internal/mirror/scan"
	"github.com/bitwise-media-group/patchy/internal/mirror/sign"
	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
	"github.com/bitwise-media-group/patchy/internal/mirror/verify"
)

// testNow anchors the injected clock.
var testNow = time.Date(2026, 11, 10, 9, 8, 7, 0, time.UTC)

const testSHA = "0123456789abcdef0123456789abcdef01234567"

// Expanded default tags under the test environment.
const (
	edgeTag = "1.4.0-edge.20261110090807.g0123456"
	shaTag  = "sha-" + testSHA
)

// fakeBuilder writes one fixed artifact into the requested OCI layout,
// so every build of a world yields the same digest.
type fakeBuilder struct {
	t     *testing.T
	index v1.ImageIndex // written when set
	image v1.Image      // written otherwise
	calls []buildx.Request
	err   error
}

func (b *fakeBuilder) Build(_ context.Context, r buildx.Request) error {
	b.calls = append(b.calls, r)
	if b.err != nil {
		return b.err
	}
	p, err := layout.Write(r.Output, empty.Index)
	if err != nil {
		return err
	}
	if b.index != nil {
		return p.AppendIndex(b.index)
	}
	return p.AppendImage(b.image)
}

// fakeScanner reports canned findings for every archive.
type fakeScanner struct {
	findings []scan.Finding
	scanned  int
}

func (s *fakeScanner) Name() string { return "fake" }

func (s *fakeScanner) ScanArchive(_ context.Context, path string) ([]scan.Finding, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	s.scanned++
	return s.findings, nil
}

// fakeSigner records signatures; the world's Verify accepts exactly those.
type fakeSigner struct {
	signed    []string
	recursive []bool
	signings  []*spec.Signing
}

// noTools reports every binary missing, so kubescape skips.
type noTools struct{}

func (noTools) Run(context.Context, string, []string, []string) ([]byte, error) {
	return nil, errors.New("unexpected tool run")
}
func (noTools) Look(string) bool { return false }

// world is one test setup: an in-memory registry, a workdir, the fakes.
type world struct {
	t       *testing.T
	host    string
	workdir string
	builder *fakeBuilder
	scanner *fakeScanner
	signer  *fakeSigner
	global  *spec.Config
	spec    *Spec
	env     map[string]string
}

// platformIndex builds a two-platform index plus an attestation manifest.
func platformIndex(t *testing.T) v1.ImageIndex {
	t.Helper()
	adds := make([]mutate.IndexAddendum, 0, 3)
	for _, p := range []v1.Platform{{OS: "linux", Architecture: "amd64"}, {OS: "linux", Architecture: "arm64"}} {
		img, err := random.Image(128, 1)
		if err != nil {
			t.Fatal(err)
		}
		adds = append(adds, mutate.IndexAddendum{Add: img, Descriptor: v1.Descriptor{Platform: &p}})
	}
	att, err := random.Image(64, 1)
	if err != nil {
		t.Fatal(err)
	}
	adds = append(adds, mutate.IndexAddendum{Add: att, Descriptor: v1.Descriptor{
		Platform:    &v1.Platform{OS: "unknown", Architecture: "unknown"},
		Annotations: map[string]string{"vnd.docker.reference.type": "attestation-manifest"},
	}})
	return mutate.AppendManifests(empty.Index, adds...)
}

// inMemoryBuilds swaps the layout loader for one that still reads the
// layout from disk — checking it round-trips to the fake builder's digest —
// but publishes the builder's in-memory copy. Pushing file-backed layers
// uses sendfile, which some sandboxes running these tests forbid.
func inMemoryBuilds(t *testing.T, b *fakeBuilder) {
	t.Helper()
	orig := loadBuilt
	t.Cleanup(func() { loadBuilt = orig })
	loadBuilt = func(dir string) (built, error) {
		fromDisk, err := loadLayout(dir)
		if err != nil {
			return built{}, err
		}
		if b.index != nil {
			d, _ := b.index.Digest()
			if fromDisk.digest != d || fromDisk.index == nil {
				t.Errorf("layout root = %s (index %v), want the built index %s", fromDisk.digest, fromDisk.index != nil, d)
			}
			return built{index: b.index, digest: d}, nil
		}
		d, _ := b.image.Digest()
		if fromDisk.digest != d || fromDisk.image == nil {
			t.Errorf("layout root = %s (image %v), want the built image %s", fromDisk.digest, fromDisk.image != nil, d)
		}
		return built{image: b.image, digest: d}, nil
	}
}

func newWorld(t *testing.T) *world {
	t.Helper()
	s := httptest.NewServer(registry.New(registry.WithReferrersSupport(true)))
	t.Cleanup(s.Close)
	u, err := url.Parse(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	w := &world{
		t:       t,
		host:    u.Host,
		workdir: t.TempDir(),
		builder: &fakeBuilder{t: t, index: platformIndex(t)},
		scanner: &fakeScanner{},
		signer:  &fakeSigner{},
		env:     map[string]string{"VERSION": "1.4.0", "GITHUB_SHA": testSHA, "GITHUB_REF_NAME": "main"},
	}
	w.setRegistryHost(w.host)
	w.spec = &Spec{
		Architectures: []string{"amd64", "arm64"},
		Tags:          []string{"${VERSION}-edge.${PATCHY_TIMESTAMP}.g${GITHUB_SHA:0:7}", "sha-${GITHUB_SHA}"},
		Images: []Image{{
			Name: "controller", Repository: "patchy/controller", Dockerfile: "Dockerfile.controller",
			Args: map[string]string{"GO_VERSION": "${GO_VERSION:-1.27}"},
		}},
		Manifests: []Manifests{{
			Name: "platform", Repository: "manifests/platform", Path: "deploy/flux", Ignore: []string{"*.md"},
			Source: "https://github.com/org/repo", Revision: "${GITHUB_REF_NAME}@sha1:${GITHUB_SHA}",
		}},
	}
	w.write("deploy/flux/kustomization.yaml", "resources: []\n")
	w.write("deploy/flux/README.md", "ignored\n")
	inMemoryBuilds(t, w.builder)
	return w
}

// setRegistryHost (re)points both registries at host: "a" signs with the
// global keyless block, "b" carries its own kms override.
func (w *world) setRegistryHost(host string) {
	w.t.Helper()
	w.global = &spec.Config{
		Registries: []spec.Registry{
			{Name: "a", URL: host + "/a"},
			{Name: "b", URL: host + "/b", Signing: &spec.Signing{
				Provider: "kms", KMS: &spec.KMSSigning{Key: "awskms://alias/b"},
			}},
		},
		Signing: spec.Signing{Provider: "keyless", Keyless: &spec.KeylessSigning{CertificateIdentity: "id"}},
	}
	if err := w.global.Resolve(); err != nil {
		w.t.Fatal(err)
	}
}

// closedRegistry points every registry at a server that is already gone:
// any registry call fails.
func (w *world) closedRegistry() {
	s := httptest.NewServer(registry.New())
	u, _ := url.Parse(s.URL)
	s.Close()
	w.setRegistryHost(u.Host)
}

func (w *world) write(rel, content string) {
	w.t.Helper()
	path := filepath.Join(w.workdir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) engine() *Engine {
	w.t.Helper()
	if err := w.spec.Validate(w.global.Scan); err != nil {
		w.t.Fatalf("spec: %v", err)
	}
	e, err := New(Config{
		Workdir:         w.workdir,
		Global:          w.global,
		Spec:            w.spec,
		Tools:           noTools{},
		ArchiveScanners: []scan.ArchiveScanner{w.scanner},
		Builder:         w.builder,
		Now:             func() time.Time { return testNow },
		LookupEnv: func(k string) (string, bool) {
			v, ok := w.env[k]
			return v, ok
		},
		Verify: func(_ context.Context, s verify.Subject) error {
			if slices.Contains(w.signer.signed, s.Ref) {
				return nil
			}
			return errors.New("unsigned")
		},
		NewSigner: func(s *spec.Signing) (sign.ArtifactSigner, error) {
			w.signer.signings = append(w.signer.signings, s)
			return signerFunc(func(ref string, recursive bool) {
				w.signer.signed = append(w.signer.signed, ref)
				w.signer.recursive = append(w.signer.recursive, recursive)
			}), nil
		},
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return e
}

// signerFunc adapts a func to sign.ArtifactSigner.
type signerFunc func(ref string, recursive bool)

func (f signerFunc) Sign(_ context.Context, ref string, recursive bool) error {
	f(ref, recursive)
	return nil
}

// actions summarizes records as "registry action ref-tag" lines.
func actions(recs []Record) []string {
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		tag := r.Ref[strings.LastIndex(r.Ref, ":")+1:]
		line := r.Registry + " " + r.Action + " " + tag
		if r.Signed {
			line += " signed"
		}
		out = append(out, line)
	}
	return out
}

func assertActions(t *testing.T, recs []Record, want ...string) {
	t.Helper()
	if got := actions(recs); !slices.Equal(got, want) {
		t.Errorf("records =\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func TestBuildImagePublishAndRerun(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	res, err := w.engine().Build(ctx, "controller", Options{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	wantDigest, _ := w.builder.index.Digest()
	if res.Digest != wantDigest.String() || !slices.Equal(res.Tags, []string{edgeTag, shaTag}) {
		t.Errorf("result digest %s tags %v", res.Digest, res.Tags)
	}
	assertActions(t, res.Records,
		"a pushed "+edgeTag+" signed", "a pushed "+shaTag+" signed",
		"b pushed "+edgeTag+" signed", "b pushed "+shaTag+" signed")
	// Two platforms scanned; the attestation manifest skipped.
	if w.scanner.scanned != 2 || !slices.Equal(res.Scan.Scanned, []string{"linux/amd64", "linux/arm64"}) {
		t.Errorf("scanned %d: %v", w.scanner.scanned, res.Scan.Scanned)
	}
	// The build request carried the expanded arg and normalized platforms.
	req := w.builder.calls[0]
	if req.Args["GO_VERSION"] != "1.27" || strings.Join(req.Platforms, ",") != "linux/amd64,linux/arm64" ||
		req.Dockerfile != filepath.Join(w.workdir, "Dockerfile.controller") || req.Context != w.workdir {
		t.Errorf("build request = %+v", req)
	}
	// The published tag is the whole index, byte for byte.
	got, err := crane.Digest(w.host + "/a/patchy/controller:" + edgeTag)
	if err != nil || got != wantDigest.String() {
		t.Errorf("published digest = %s, %v", got, err)
	}
	// Per-registry signing: a under the global keyless block, b under its
	// kms override; images sign recursively.
	if len(w.signer.signings) != 2 || w.signer.signings[0].Provider != "keyless" ||
		w.signer.signings[1].Provider != "kms" || !w.signer.recursive[0] {
		t.Errorf("signings = %+v recursive %v", w.signer.signings, w.signer.recursive)
	}
	if w.signer.signed[0] != w.host+"/a/patchy/controller@"+wantDigest.String() {
		t.Errorf("signed %v", w.signer.signed)
	}

	// Re-run: everything current, nothing re-signed.
	res, err = w.engine().Build(ctx, "controller", Options{})
	if err != nil {
		t.Fatalf("re-run: %v", err)
	}
	assertActions(t, res.Records,
		"a skipped-current "+edgeTag, "a skipped-current "+shaTag,
		"b skipped-current "+edgeTag, "b skipped-current "+shaTag)
	if len(w.signer.signed) != 2 {
		t.Errorf("re-run signed again: %v", w.signer.signed)
	}
}

// TestBuildTagsNeverMove pins the immutable-tag contract: a tag already
// holding another digest is reported and left alone.
func TestBuildTagsNeverMove(t *testing.T) {
	w := newWorld(t)
	other, err := random.Image(64, 1)
	if err != nil {
		t.Fatal(err)
	}
	otherRef := w.host + "/a/patchy/controller:" + shaTag
	if err := crane.Push(other, otherRef); err != nil {
		t.Fatal(err)
	}
	otherDigest, _ := other.Digest()
	res, err := w.engine().Build(context.Background(), "controller", Options{Registries: []string{"a"}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	assertActions(t, res.Records, "a pushed "+edgeTag+" signed", "a skipped-tag-exists "+shaTag)
	if res.Records[1].Digest != otherDigest.String() {
		t.Errorf("tag-exists record digest = %s, want the other digest", res.Records[1].Digest)
	}
	if got, _ := crane.Digest(otherRef); got != otherDigest.String() {
		t.Errorf("tag moved to %s", got)
	}
}

// finding builds a fixed, blocking-shaped test finding.
func finding(id, severity string) scan.Finding {
	return scan.Finding{ID: id, Severity: severity, Package: "lib-" + strings.ToLower(id), Installed: "1",
		FixedIn: []string{"2"}, Scanner: "fake"}
}

func TestBuildBlockingFindingPushesNothing(t *testing.T) {
	w := newWorld(t)
	w.scanner.findings = []scan.Finding{finding("CVE-CRIT", "CRITICAL")}
	res, err := w.engine().Build(context.Background(), "controller", Options{})
	if err == nil || !strings.Contains(err.Error(), "nothing pushed") {
		t.Fatalf("Build = %v, want the scan gate", err)
	}
	if len(res.Records) != 0 || len(res.Scan.Blocking) != 1 {
		t.Errorf("records %v blocking %v", res.Records, res.Scan.Blocking)
	}
	if _, err := crane.Digest(w.host + "/a/patchy/controller:" + shaTag); err == nil {
		t.Error("a blocked build reached the registry")
	}
}

func TestBuildAllowlist(t *testing.T) {
	t.Run("suppresses, and a lapsed entry does not", func(t *testing.T) {
		w := newWorld(t)
		w.scanner.findings = []scan.Finding{finding("CVE-CRIT", "CRITICAL"), finding("CVE-LAPSED", "CRITICAL")}
		w.write("security/allowlists/controller.yaml", `vulnerabilities:
  - id: CVE-CRIT
    statement: accepted
    expired_at: "2026-12-01"
  - id: CVE-LAPSED
    statement: accepted once
    expired_at: "2026-11-01"
`)
		res, err := w.engine().Build(context.Background(), "controller", Options{})
		if err == nil {
			t.Fatal("Build passed with a lapsed acceptance")
		}
		if res.Scan.Suppressed != 1 || len(res.Scan.Blocking) != 1 || res.Scan.Blocking[0].ID != "CVE-LAPSED" ||
			!slices.Equal(res.Scan.Expired, []string{"CVE-LAPSED"}) {
			t.Errorf("scan = %+v", res.Scan)
		}
	})

	t.Run("beyond the horizon fails before building", func(t *testing.T) {
		w := newWorld(t)
		w.write("security/allowlists/controller.yaml", `vulnerabilities:
  - id: CVE-CRIT
    statement: accepted
    expired_at: "2027-12-01"
`)
		_, err := w.engine().Build(context.Background(), "controller", Options{})
		if err == nil || !strings.Contains(err.Error(), "expires more than 90 days out") ||
			!strings.Contains(err.Error(), filepath.Join("security", "allowlists", "controller.yaml")) {
			t.Fatalf("Build = %v, want the horizon violation", err)
		}
		if len(w.builder.calls) != 0 {
			t.Error("built despite a broken allowlist")
		}
	})
}

func TestBuildScanPolicy(t *testing.T) {
	t.Run("entry failOn overrides the config", func(t *testing.T) {
		w := newWorld(t)
		w.scanner.findings = []scan.Finding{finding("CVE-HIGH", "HIGH")}
		w.global.Scan.FailOn = []string{"CRITICAL", "HIGH"}
		opts := Options{Registries: []string{"a"}}
		if _, err := w.engine().Build(context.Background(), "controller", opts); err == nil {
			t.Fatal("HIGH did not block under the config's failOn")
		}
		w.spec.Images[0].Scan.FailOn = []string{"CRITICAL"}
		if _, err := w.engine().Build(context.Background(), "controller", opts); err != nil {
			t.Fatalf("Build with entry failOn [CRITICAL]: %v", err)
		}
	})

	t.Run("no scanner enabled is an error", func(t *testing.T) {
		w := newWorld(t)
		e := w.engine()
		e.archiveScanners = nil
		if _, err := e.Build(context.Background(), "controller", Options{}); err == nil ||
			!strings.Contains(err.Error(), "no image scanner enabled") {
			t.Fatalf("Build = %v", err)
		}
	})

	t.Run("scan disabled skips the scanners and the allowlist", func(t *testing.T) {
		w := newWorld(t)
		w.scanner.findings = []scan.Finding{finding("CVE-CRIT", "CRITICAL")}
		w.spec.Images[0].Scan.Enabled = spec.ScanOff
		w.write("security/allowlists/controller.yaml", "vulnerabilities: [{id: X}]\n")
		res, err := w.engine().Build(context.Background(), "controller", Options{Registries: []string{"a"}})
		if err != nil || w.scanner.scanned != 0 || !strings.Contains(res.Scan.Skipped, "the entry's scan.enabled: false") {
			t.Fatalf("Build = %v, scanned %d, report %+v", err, w.scanner.scanned, res.Scan)
		}
	})
}

func TestBuildRegistrySelectionAndTags(t *testing.T) {
	w := newWorld(t)
	res, err := w.engine().Build(context.Background(), "controller",
		Options{Registries: []string{"b"}, Tags: []string{"v${VERSION}", "v${VERSION}"}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	assertActions(t, res.Records, "b pushed v1.4.0 signed")

	if _, err := w.engine().Build(context.Background(), "controller", Options{Registries: []string{"nope"}}); err == nil ||
		!strings.Contains(err.Error(), `unknown registry "nope"`) {
		t.Errorf("unknown registry = %v", err)
	}

	calls := len(w.builder.calls)
	for _, tt := range []struct{ tag, want string }{
		{"${UNSET}", "${UNSET} is not set"},
		{"bad tag ${VERSION}", "not a valid OCI tag"},
		{"$VERSION", "bare $"},
	} {
		_, err := w.engine().Build(context.Background(), "controller", Options{Tags: []string{tt.tag}})
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("tag %q: %v, want %q", tt.tag, err, tt.want)
		}
	}
	if len(w.builder.calls) != calls {
		t.Error("a bad tag reached the builder: expansion must fail fast")
	}

	w.spec.Tags = nil
	if _, err := w.engine().Build(context.Background(), "controller", Options{}); err == nil ||
		!strings.Contains(err.Error(), "no tags") {
		t.Errorf("no tags = %v", err)
	}
}

// TestBuildDryRunNeverContactsARegistry points every registry at a closed
// server: any registry call would fail the run.
func TestBuildDryRunNeverContactsARegistry(t *testing.T) {
	w := newWorld(t)
	w.closedRegistry()
	for _, name := range []string{"controller", "platform"} {
		res, err := w.engine().Build(context.Background(), name, Options{DryRun: true})
		if err != nil {
			t.Fatalf("%s dry run: %v", name, err)
		}
		assertActions(t, res.Records,
			"a would-push "+edgeTag, "a would-push "+shaTag,
			"b would-push "+edgeTag, "b would-push "+shaTag)
	}
	if w.scanner.scanned != 2 || len(w.signer.signings) != 0 {
		t.Errorf("dry run scanned %d, signed %v", w.scanner.scanned, w.signer.signed)
	}
}

func TestBuildManifests(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	res, err := w.engine().Build(ctx, "platform", Options{Registries: []string{"a"}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	assertActions(t, res.Records, "a pushed "+edgeTag+" signed", "a pushed "+shaTag+" signed")
	if res.Scan.ConfigScan != "skipped: kubescape not on PATH" {
		t.Errorf("config scan = %q", res.Scan.ConfigScan)
	}
	raw, err := crane.Manifest(w.host + "/a/manifests/platform:" + shaTag)
	if err != nil {
		t.Fatal(err)
	}
	var m v1.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m.Config.MediaType != fluxartifact.ConfigMediaType || m.Layers[0].MediaType != fluxartifact.ContentMediaType ||
		m.Annotations[fluxartifact.RevisionAnnotation] != "main@sha1:"+testSHA ||
		m.Annotations[fluxartifact.SourceAnnotation] != "https://github.com/org/repo" {
		t.Errorf("manifest = %s", raw)
	}
	// Manifests sign non-recursively, and a re-run is a no-op.
	if len(w.signer.recursive) != 1 || w.signer.recursive[0] {
		t.Errorf("recursive = %v", w.signer.recursive)
	}
	res, err = w.engine().Build(ctx, "platform", Options{Registries: []string{"a"}})
	if err != nil {
		t.Fatalf("re-run: %v", err)
	}
	assertActions(t, res.Records, "a skipped-current "+edgeTag, "a skipped-current "+shaTag)

	// A disabled entry skips even the configuration scan.
	w.spec.Manifests[0].Scan.Enabled = spec.ScanOff
	res, err = w.engine().Build(ctx, "platform", Options{DryRun: true})
	if err != nil || !strings.Contains(res.Scan.ConfigScan, "scanning disabled") {
		t.Errorf("disabled manifests = %+v, %v", res.Scan, err)
	}
	// An unset annotation variable fails before anything is packaged.
	w.spec.Manifests[0].Revision = "${MISSING_REF}"
	if _, err := w.engine().Build(ctx, "platform", Options{DryRun: true}); err == nil ||
		!strings.Contains(err.Error(), "revision") {
		t.Errorf("unset revision = %v", err)
	}
}

func TestBuildSinglePlatformImage(t *testing.T) {
	w := newWorld(t)
	img, err := random.Image(128, 1)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := img.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	cfg = cfg.DeepCopy()
	cfg.OS, cfg.Architecture = "linux", "amd64"
	img, err = mutate.ConfigFile(img, cfg)
	if err != nil {
		t.Fatal(err)
	}
	w.builder.index, w.builder.image = nil, img
	res, err := w.engine().Build(context.Background(), "controller", Options{Registries: []string{"a"}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	want, _ := img.Digest()
	if res.Digest != want.String() || !slices.Equal(res.Scan.Scanned, []string{"linux/amd64"}) {
		t.Errorf("result = %+v", res)
	}
	if got, _ := crane.Digest(w.host + "/a/patchy/controller:" + shaTag); got != want.String() {
		t.Errorf("published %s", got)
	}
}

func TestUpdateAllowlist(t *testing.T) {
	w := newWorld(t)
	w.closedRegistry()
	w.scanner.findings = []scan.Finding{finding("CVE-NEW", "CRITICAL"), finding("CVE-LOW", "LOW")}
	ctx := context.Background()

	res, err := w.engine().UpdateAllowlist(ctx, "controller")
	if err != nil || res.Generated || !strings.Contains(res.Note, "generate is false") || len(w.builder.calls) != 0 {
		t.Fatalf("generate false = %+v, %v (builder calls %d)", res, err, len(w.builder.calls))
	}
	if res, err := w.engine().UpdateAllowlist(ctx, "platform"); err != nil || res.Generated {
		t.Errorf("manifests entry = %+v, %v", res, err)
	}

	yes, preamble := true, "Built from main."
	w.spec.Images[0].Scan.Allowlist = spec.AllowlistPolicy{Generate: &yes, Preamble: &preamble}
	w.write("security/allowlists/controller.yaml", `vulnerabilities:
  - id: CVE-GONE
    statement: s
    expired_at: "2026-12-01"
`)
	res, err = w.engine().UpdateAllowlist(ctx, "controller")
	if err != nil {
		t.Fatalf("UpdateAllowlist: %v", err)
	}
	if !res.Generated || res.Added != 1 || !slices.Equal(res.Dropped, []string{"CVE-GONE"}) ||
		res.Path != filepath.Join("security", "allowlists", "controller.yaml") {
		t.Errorf("result = %+v", res)
	}
	raw, err := os.ReadFile(filepath.Join(w.workdir, "security", "allowlists", "controller.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"GENERATED by patchy build", "# Built from main.", "id: CVE-NEW", `expired_at: "2027-02-08"`,
	} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("allowlist lacks %q:\n%s", want, raw)
		}
	}
	if strings.Contains(string(raw), "CVE-LOW") {
		t.Error("a non-blocking severity was accepted into the allowlist")
	}
	if len(w.signer.signings) != 0 {
		t.Error("UpdateAllowlist signed something")
	}
}

func TestSpecValidate(t *testing.T) {
	base := func() *Spec {
		return &Spec{
			Architectures: []string{"amd64"},
			Images:        []Image{{Name: "controller", Repository: "patchy/controller"}},
			Manifests:     []Manifests{{Name: "platform", Repository: "manifests/platform", Path: "deploy"}},
		}
	}
	yes := true
	tests := []struct {
		name    string
		mutate  func(*Spec)
		wantErr string
	}{
		{name: "valid", mutate: func(*Spec) {}},
		{
			name:    "duplicate across kinds",
			mutate:  func(s *Spec) { s.Manifests[0].Name = "controller" },
			wantErr: "duplicate name",
		},
		{name: "bad name", mutate: func(s *Spec) { s.Images[0].Name = "Controller" }, wantErr: "name"},
		{
			name:    "missing repository",
			mutate:  func(s *Spec) { s.Images[0].Repository = "" },
			wantErr: "repository is required",
		},
		{
			name:    "bad repository",
			mutate:  func(s *Spec) { s.Images[0].Repository = "Patchy//x" },
			wantErr: "not a valid OCI repository",
		},
		{name: "no architectures", mutate: func(s *Spec) { s.Architectures = nil }, wantErr: "no architectures"},
		{
			name:    "bad architecture",
			mutate:  func(s *Spec) { s.Images[0].Architectures = []string{"linux amd64"} },
			wantErr: "architecture",
		},
		{name: "missing path", mutate: func(s *Spec) { s.Manifests[0].Path = "" }, wantErr: "path is required"},
		{name: "empty tag", mutate: func(s *Spec) { s.Tags = []string{" "} }, wantErr: "tags[0] is empty"},
		{name: "bad enabled", mutate: func(s *Spec) { s.Images[0].Scan.Enabled = "maybe" }, wantErr: "scan.enabled"},
		{
			name:    "manifests entry with an allowlist",
			mutate:  func(s *Spec) { s.Manifests[0].Scan.Allowlist.Generate = &yes },
			wantErr: "apply to image scans only",
		},
		{
			name:    "manifests entry with failOn",
			mutate:  func(s *Spec) { s.Manifests[0].Scan.FailOn = []string{"CRITICAL"} },
			wantErr: "apply to image scans only",
		},
		{name: "manifests entry may set enabled", mutate: func(s *Spec) { s.Manifests[0].Scan.Enabled = spec.ScanOff }},
		{
			name: "image newDays past maxDays",
			mutate: func(s *Spec) {
				n := 120
				s.Images[0].Scan.Allowlist.NewDays = &n
			},
			wantErr: "newDays (120) exceeds",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := base()
			tt.mutate(s)
			err := s.Validate(spec.Scan{})
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Validate = %v, want %q", err, tt.wantErr)
			}
		})
	}
	// Defaults: dockerfile under the context, context ".".
	img := Image{Context: "src"}
	if img.DockerfilePath() != filepath.Join("src", "Dockerfile") || (&Image{}).ContextDir() != "." {
		t.Errorf("defaults: %q %q", img.DockerfilePath(), (&Image{}).ContextDir())
	}
}

// TestLoadLayout reads both root shapes buildx exports, and the platform
// images inside them, straight from disk.
func TestLoadLayout(t *testing.T) {
	idx := platformIndex(t)
	dir := filepath.Join(t.TempDir(), "oci")
	if err := (&fakeBuilder{index: idx}).Build(context.Background(), buildx.Request{Output: dir}); err != nil {
		t.Fatal(err)
	}
	b, err := loadLayout(dir)
	if err != nil {
		t.Fatalf("loadLayout: %v", err)
	}
	want, _ := idx.Digest()
	if b.index == nil || b.digest != want {
		t.Fatalf("loaded %+v, want index %s", b, want)
	}
	images, err := b.platformImages()
	if err != nil || len(images) != 2 || images[0].platform != "linux/amd64" || images[1].platform != "linux/arm64" {
		t.Fatalf("platform images = %+v, %v", images, err)
	}
	archive := filepath.Join(t.TempDir(), "scan.tar")
	if err := images[0].saveArchive(archive); err != nil {
		t.Fatalf("saveArchive: %v", err)
	}

	img, err := random.Image(64, 1)
	if err != nil {
		t.Fatal(err)
	}
	dir = filepath.Join(t.TempDir(), "oci")
	if err := (&fakeBuilder{image: img}).Build(context.Background(), buildx.Request{Output: dir}); err != nil {
		t.Fatal(err)
	}
	if b, err = loadLayout(dir); err != nil || b.image == nil {
		t.Fatalf("single image layout = %+v, %v", b, err)
	}
	if _, err := loadLayout(t.TempDir()); err == nil {
		t.Error("an empty directory loaded as a build")
	}
}

// TestPlatformImagesWithoutPlatform pins the fallback for a lone image
// whose config names no os/arch: scanned as "unknown", never a panic.
func TestPlatformImagesWithoutPlatform(t *testing.T) {
	img, err := random.Image(64, 1)
	if err != nil {
		t.Fatal(err)
	}
	images, err := built{image: img}.platformImages()
	if err != nil || len(images) != 1 || images[0].platform != "unknown" {
		t.Errorf("platformImages = %+v, %v", images, err)
	}
}
