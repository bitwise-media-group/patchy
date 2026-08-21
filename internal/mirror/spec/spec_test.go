// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package spec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	c, err := LoadConfig("testdata/store/mirror.yaml")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	assertConfigRegistries(t, c)
	if c.Signing.Provider != "keyless" || c.Signing.Keyless == nil {
		t.Errorf("signing = %+v", c.Signing)
	}
	if !c.Signing.Keyless.TlogUploadEnabled() {
		t.Error("tlogUpload should resolve true")
	}
	if got := c.Update.EffectiveCooldownDays(); got != 3 {
		t.Errorf("cooldownDays = %d", got)
	}
	if !c.Scan.Scanners.OSVEnabled() || c.Scan.Scanners.GrypeEnabled() || !c.Scan.Scanners.KubescapeEnabled() {
		t.Errorf("scanners = %+v", c.Scan.Scanners)
	}
	if c.Scan.Scanners.KubescapeMode() != "warn" {
		t.Errorf("kubescape mode = %q", c.Scan.Scanners.KubescapeMode())
	}
	if c.SourceRegistryRewrites["docker.io"] != "mirror.example.com/dockerhub" {
		t.Errorf("rewrites = %v", c.SourceRegistryRewrites)
	}
}

// assertConfigRegistries checks the registries list decoded fully, with
// per-element namespace defaults and per-registry effective signing.
func assertConfigRegistries(t *testing.T, c *Config) {
	t.Helper()
	if len(c.Registries) != 2 {
		t.Fatalf("registries = %+v", c.Registries)
	}
	primary, ghcr := c.Registries[0], c.Registries[1]
	if primary.Name != "primary" || primary.URL != "registry.example.com/org/platform" {
		t.Errorf("primary = %+v", primary)
	}
	// artifactNamespace is absent from the file; the default fills in —
	// per element (ghcr declares no namespaces at all).
	if primary.ArtifactNamespace != "artifacts" {
		t.Errorf("artifactNamespace = %q, want default artifacts", primary.ArtifactNamespace)
	}
	if ghcr.Name != "ghcr" || ghcr.ChartNamespace != "charts" || ghcr.ImageNamespace != "images" ||
		ghcr.ArtifactNamespace != "artifacts" {
		t.Errorf("ghcr defaults = %+v", ghcr)
	}
	// Effective signing: a registry's own block wins wholesale, the rest
	// fall back to the global default.
	if s := primary.EffectiveSigning(&c.Signing); s != &c.Signing {
		t.Errorf("primary effective signing = %+v, want the global block", s)
	}
	if s := ghcr.EffectiveSigning(&c.Signing); s.Provider != "kms" || s.KMS == nil ||
		s.KMS.Key != "gcpkms://projects/p/locations/l/keyRings/r/cryptoKeys/k" {
		t.Errorf("ghcr effective signing = %+v, want its own kms block", s)
	}
}

func TestScanDefaults(t *testing.T) {
	var s Scan
	if got := strings.Join(s.EffectiveFailOn(), ","); got != "CRITICAL,HIGH" {
		t.Errorf("failOn default = %q", got)
	}
	if !s.EffectiveIgnoreUnfixed() {
		t.Error("ignoreUnfixed default should be true")
	}
	if s.EffectiveAllowlistMaxDays() != 90 || s.EffectiveAllowlistNewDays() != 90 {
		t.Error("allowlist day defaults should be 90")
	}
	if !s.EffectiveEnabled() {
		t.Error("scan enabled default should be true")
	}
	disabled := false
	if (Scan{Enabled: &disabled}).EffectiveEnabled() {
		t.Error("scan.enabled: false should disable scanning")
	}
	var sc Scanners
	if sc.OSVEnabled() || sc.GrypeEnabled() || !sc.KubescapeEnabled() || sc.KubescapeMode() != "warn" {
		t.Errorf("zero-value scanner defaults wrong: %+v", sc)
	}
	if !(Scanners{OSV: &ScannerToggle{Enabled: true}}).OSVEnabled() {
		t.Error("osv.enabled: true should enable osv")
	}
}

func TestLoadConfigRejects(t *testing.T) {
	write := func(t *testing.T, content string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "mirror.yaml")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	registries := "registries: [{name: r, url: r.example.com/x}]\n"
	tests := []struct {
		name, content, wantErr string
	}{
		{
			name:    "wrong kind",
			content: "apiVersion: mirror.patchy.bitwisemedia.uk/v1alpha1\nkind: Chart\n" + registries,
			wantErr: "kind",
		},
		{
			name:    "wrong apiVersion",
			content: "apiVersion: flux-containers/v1alpha1\nkind: MirrorConfig\n" + registries,
			wantErr: "apiVersion",
		},
		{
			name:    "no registries",
			content: "apiVersion: mirror.patchy.bitwisemedia.uk/v1alpha1\nkind: MirrorConfig\n",
			wantErr: "at least one registry",
		},
		{
			name: "legacy singular registry block",
			content: "apiVersion: mirror.patchy.bitwisemedia.uk/v1alpha1\nkind: MirrorConfig\n" +
				"registry: {url: r.example.com/x}\n",
			wantErr: "registry",
		},
		{
			name: "missing registry name",
			content: "apiVersion: mirror.patchy.bitwisemedia.uk/v1alpha1\nkind: MirrorConfig\n" +
				"registries: [{url: r.example.com/x}]\n",
			wantErr: "name",
		},
		{
			name: "bad registry name charset",
			content: "apiVersion: mirror.patchy.bitwisemedia.uk/v1alpha1\nkind: MirrorConfig\n" +
				"registries: [{name: \"Prod Registry\", url: r.example.com/x}]\n",
			wantErr: "name",
		},
		{
			name: "missing registry url",
			content: "apiVersion: mirror.patchy.bitwisemedia.uk/v1alpha1\nkind: MirrorConfig\n" +
				"registries: [{name: r}]\n",
			wantErr: "url is required",
		},
		{
			name: "duplicate registry names",
			content: "apiVersion: mirror.patchy.bitwisemedia.uk/v1alpha1\nkind: MirrorConfig\n" +
				"registries: [{name: r, url: a.example.com/x}, {name: r, url: b.example.com/x}]\n",
			wantErr: "duplicate name",
		},
		{
			name: "invalid per-registry signing",
			content: "apiVersion: mirror.patchy.bitwisemedia.uk/v1alpha1\nkind: MirrorConfig\n" +
				"registries: [{name: r, url: r.example.com/x, signing: {provider: kms}}]\n",
			wantErr: "kms.key",
		},
		{
			name: "unknown field",
			content: "apiVersion: mirror.patchy.bitwisemedia.uk/v1alpha1\nkind: MirrorConfig\n" +
				registries + "bogus: true\n",
			wantErr: "bogus",
		},
		{
			name: "kms without key",
			content: "apiVersion: mirror.patchy.bitwisemedia.uk/v1alpha1\nkind: MirrorConfig\n" +
				registries + "signing: {provider: kms}\n",
			wantErr: "kms.key",
		},
		{
			name: "newDays beyond maxDays",
			content: "apiVersion: mirror.patchy.bitwisemedia.uk/v1alpha1\nkind: MirrorConfig\n" +
				registries + "scan: {allowlistMaxDays: 30, allowlistNewDays: 60}\n",
			wantErr: "allowlistNewDays",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadConfig(write(t, tt.content))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("want error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestSelectRegistries(t *testing.T) {
	c, err := LoadConfig("testdata/store/mirror.yaml")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	names := func(regs []Registry) string {
		out := make([]string, len(regs))
		for i, r := range regs {
			out[i] = r.Name
		}
		return strings.Join(out, ",")
	}
	tests := []struct {
		name    string
		sel     []string
		want    string // comma-joined result; "" with wantErr set = error
		wantErr string
	}{
		{name: "nil selects all", sel: nil, want: "primary,ghcr"},
		{name: "empty selects all", sel: []string{}, want: "primary,ghcr"},
		{name: "subset", sel: []string{"ghcr"}, want: "ghcr"},
		{name: "declaration order regardless of selection order", sel: []string{"ghcr", "primary"}, want: "primary,ghcr"},
		{name: "unknown name", sel: []string{"nope"}, wantErr: `unknown registry "nope"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := c.SelectRegistries(tt.sel)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want one containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("SelectRegistries: %v", err)
			}
			if names(got) != tt.want {
				t.Errorf("selected = %q, want %q", names(got), tt.want)
			}
		})
	}
}

func TestDiscover(t *testing.T) {
	entries, err := Discover("testdata/store")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	// Sorted by name: bundle (artifact) before demo (chart).
	if entries[0].Name != "bundle" || entries[0].Kind != KindArtifact || entries[0].Artifact == nil {
		t.Errorf("entries[0] = %+v", entries[0])
	}
	if entries[1].Name != "demo" || entries[1].Kind != KindChart || entries[1].Chart == nil {
		t.Errorf("entries[1] = %+v", entries[1])
	}
	assertDemoChart(t, entries[1])
	assertBundleArtifact(t, entries[0])
}

// assertDemoChart checks the chart fixture decoded fully.
func assertDemoChart(t *testing.T, demo Entry) {
	t.Helper()
	m := demo.Chart
	if m.Chart.Lockstep != "pair" || demo.Lockstep() != "pair" {
		t.Errorf("lockstep = %q/%q", m.Chart.Lockstep, demo.Lockstep())
	}
	if demo.Version() != "1.2.3" || demo.VersionConstraint() != ">=1.0.0 <2.0.0" {
		t.Errorf("version = %q constraint = %q", demo.Version(), demo.VersionConstraint())
	}
	if len(m.Images.VerifyUpstream) != 3 {
		t.Fatalf("verify rules = %d", len(m.Images.VerifyUpstream))
	}
	// Inline VerifyRule fields must decode next to Match.
	r := m.Images.VerifyUpstream[1]
	if r.Match != "quay.io/legacy/*" || r.Provider != "cosign-key" || r.SignatureDigestAlgorithm != "sha512" {
		t.Errorf("inline rule = %+v", r)
	}
	if m.Images.VerifyUpstream[0].EffectiveOidcIssuer() != defaultOidcIssuer {
		t.Errorf("issuer = %q", m.Images.VerifyUpstream[0].EffectiveOidcIssuer())
	}
	if m.Chart.VerifyUpstream.EffectiveOidcIssuer() != defaultOidcIssuer {
		t.Error("chart rule issuer default")
	}
	if m.Scan == nil || !m.Scan.Allowlist.Generate || !strings.Contains(m.Scan.Allowlist.Preamble, "Whole-file") {
		t.Errorf("scan override = %+v", m.Scan)
	}
	if got := filepath.Base(demo.LockPath()); got != "images.lock.yaml" {
		t.Errorf("chart lock path = %q", got)
	}
}

// assertBundleArtifact checks the artifact fixture decoded fully.
func assertBundleArtifact(t *testing.T, bundle Entry) {
	t.Helper()
	if bundle.Artifact.Artifact.Ref != "ghcr.io/example/bundle" {
		t.Errorf("artifact ref = %q", bundle.Artifact.Artifact.Ref)
	}
	if cd := bundle.Artifact.Artifact.CooldownDays; cd == nil || *cd != 0 {
		t.Errorf("cooldownDays = %v, want explicit 0", cd)
	}
	if bundle.Artifact.Scan.EffectiveEnabled() != "auto" {
		t.Errorf("scan enabled = %q", bundle.Artifact.Scan.EffectiveEnabled())
	}
	if got := filepath.Base(bundle.LockPath()); got != "lock.yaml" {
		t.Errorf("artifact lock path = %q", got)
	}
}

func TestLoadEntry(t *testing.T) {
	e, err := LoadEntry("testdata/store", "demo")
	if err != nil {
		t.Fatalf("LoadEntry: %v", err)
	}
	if e.Kind != KindChart || e.Chart == nil {
		t.Errorf("entry = %+v", e)
	}
	if _, err := LoadEntry("testdata/store", "missing"); err == nil {
		t.Error("want error for missing entry")
	}
}

func TestFindRoot(t *testing.T) {
	root, err := filepath.Abs("testdata/store")
	if err != nil {
		t.Fatal(err)
	}
	got, err := FindRoot(filepath.Join(root, "charts", "demo"))
	if err != nil {
		t.Fatalf("FindRoot: %v", err)
	}
	if got != root {
		t.Errorf("FindRoot = %q, want %q", got, root)
	}
	if _, err := FindRoot(t.TempDir()); err == nil {
		t.Error("want error when no mirror.yaml exists upward")
	}
}

func TestEffectiveScanPolicy(t *testing.T) {
	var global Scan
	p := EffectiveScanPolicy(global, nil)
	if strings.Join(p.FailOn, ",") != "CRITICAL,HIGH" || !p.IgnoreUnfixed {
		t.Errorf("global policy = %+v", p)
	}
	f := false
	p = EffectiveScanPolicy(global, &ScanOverride{FailOn: []string{"CRITICAL"}, IgnoreUnfixed: &f})
	if strings.Join(p.FailOn, ",") != "CRITICAL" || p.IgnoreUnfixed {
		t.Errorf("override policy = %+v", p)
	}
}

// The lock emitter must reproduce the committed lock files byte-for-byte:
// load a real fixture, re-encode, compare bytes.
func TestImagesLockRoundTrip(t *testing.T) {
	for _, fixture := range []string{
		"testdata/otel.images.lock.yaml",
		"testdata/certmanager.images.lock.yaml",
	} {
		t.Run(filepath.Base(fixture), func(t *testing.T) {
			raw, err := os.ReadFile(fixture)
			if err != nil {
				t.Fatal(err)
			}
			l, err := LoadImagesLock(fixture)
			if err != nil {
				t.Fatalf("LoadImagesLock: %v", err)
			}
			if got := l.Encode(); string(got) != string(raw) {
				t.Errorf("round trip differs:\n--- got ---\n%s\n--- want ---\n%s", got, raw)
			}
		})
	}
}

func TestImagesLockEmpty(t *testing.T) {
	l := &ImagesLock{Chart: LockChart{Name: "n", Version: "1.0.0", UpstreamTgzSha256: "abc"}}
	want := "chart:\n  name: n\n  version: 1.0.0\n  upstreamTgzSha256: abc\nimages: []\n"
	if got := string(l.Encode()); got != want {
		t.Errorf("empty lock:\n%s", got)
	}
}

func TestArtifactLockRoundTrip(t *testing.T) {
	l := &ArtifactLock{Artifact: LockArtifact{
		Ref:     "ghcr.io/example/bundle",
		Version: "0.5.0",
		Digest:  "sha256:abc",
		Targets: map[string]string{
			// Two keys pin the sorted emission.
			"primary": "registry.example.com/org/platform/artifacts/ghcr.io/example/bundle:0.5.0",
			"ghcr":    "ghcr.io/org/platform/artifacts/ghcr.io/example/bundle:0.5.0",
		},
		Platforms: []string{"linux/amd64", "linux/arm64"},
	}}
	enc := l.Encode()
	if !strings.Contains(string(enc), "  targets:\n    ghcr: ghcr.io/org/platform/artifacts/"+
		"ghcr.io/example/bundle:0.5.0\n    primary: registry.example.com/org/platform/artifacts/"+
		"ghcr.io/example/bundle:0.5.0\n") {
		t.Errorf("targets not emitted sorted by name:\n%s", enc)
	}
	path := filepath.Join(t.TempDir(), "lock.yaml")
	if err := os.WriteFile(path, enc, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadArtifactLock(path)
	if err != nil {
		t.Fatalf("LoadArtifactLock: %v", err)
	}
	if got.Artifact.Ref != l.Artifact.Ref || len(got.Artifact.Platforms) != 2 ||
		len(got.Artifact.Targets) != 2 {
		t.Errorf("round trip = %+v", got)
	}
	if string(got.Encode()) != string(enc) {
		t.Error("re-encode differs")
	}
}

// A pre-registries lock (singular target:) must fail strict decode with a
// hint at the upgrade path, for both lock flavours.
func TestLegacyLockHint(t *testing.T) {
	dir := t.TempDir()
	imagesLock := filepath.Join(dir, "images.lock.yaml")
	if err := os.WriteFile(imagesLock, []byte("chart:\n  name: demo\n  version: 1.0.0\n"+
		"  upstreamTgzSha256: abc\nimages:\n  - source: ghcr.io/x/y:1.0.0\n    digest: sha256:abc\n"+
		"    target: registry.example.com/images/ghcr.io/x/y:1.0.0\n    platforms: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadImagesLock(imagesLock); err == nil ||
		!strings.Contains(err.Error(), "predates the multi-registry schema") {
		t.Errorf("images lock error = %v, want the upgrade hint", err)
	}
	artifactLock := filepath.Join(dir, "lock.yaml")
	if err := os.WriteFile(artifactLock, []byte("artifact:\n  ref: ghcr.io/x/y\n  version: 1.0.0\n"+
		"  digest: sha256:abc\n  target: registry.example.com/artifacts/ghcr.io/x/y:1.0.0\n"+
		"  platforms: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadArtifactLock(artifactLock); err == nil ||
		!strings.Contains(err.Error(), "predates the multi-registry schema") {
		t.Errorf("artifact lock error = %v, want the upgrade hint", err)
	}
}

func TestLoadAllowlist(t *testing.T) {
	a, err := LoadAllowlist("testdata/store/charts/demo")
	if err != nil {
		t.Fatalf("LoadAllowlist: %v", err)
	}
	if len(a.Vulnerabilities) != 2 {
		t.Fatalf("entries = %d", len(a.Vulnerabilities))
	}
	e := a.Vulnerabilities[0]
	if e.ID != "GO-2026-4970" || e.ExpiredAt != "2026-10-01" || !strings.Contains(e.Notes, "dind") {
		t.Errorf("entry = %+v", e)
	}
	// Missing file is an empty allowlist.
	empty, err := LoadAllowlist(t.TempDir())
	if err != nil || len(empty.Vulnerabilities) != 0 {
		t.Errorf("missing allowlist = %+v, %v", empty, err)
	}
}

func TestSidecar(t *testing.T) {
	s := &Sidecar{Extra: []ExtraImage{{Image: "ghcr.io/x/y:v1", Reason: "derived"}}}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, SidecarFile), s.Encode(), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSidecar(dir)
	if err != nil {
		t.Fatalf("LoadSidecar: %v", err)
	}
	if len(got.Extra) != 1 || got.Extra[0].Image != "ghcr.io/x/y:v1" {
		t.Errorf("sidecar = %+v", got)
	}
	// Missing sidecar is empty.
	empty, err := LoadSidecar(t.TempDir())
	if err != nil || len(empty.Extra) != 0 {
		t.Errorf("missing sidecar = %+v, %v", empty, err)
	}
}

func TestNameMismatchRejected(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "charts", "renamed")
	if err := os.MkdirAll(entry, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "apiVersion: mirror.patchy.bitwisemedia.uk/v1alpha1\nkind: Chart\nname: other\n" +
		"chart: {repo: oci://x/y, name: other, version: \"1.0.0\"}\n"
	if err := os.WriteFile(filepath.Join(entry, "manifest.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Discover(dir); err == nil || !strings.Contains(err.Error(), "match its directory") {
		t.Errorf("want name-mismatch error, got %v", err)
	}
}
