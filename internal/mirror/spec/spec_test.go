// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package spec

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// loadConfig decodes a Config fixture strictly and resolves it — the same
// two steps the CLI's layered config performs before handing a Config in.
func loadConfig(t *testing.T, path string) (*Config, error) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var c Config
	if err := decodeStrict(raw, &c); err != nil {
		return nil, err
	}
	if err := c.Resolve(); err != nil {
		return nil, err
	}
	return &c, nil
}

func TestConfigResolve(t *testing.T) {
	c, err := loadConfig(t, "testdata/config.yaml")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
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
	if a := EffectiveScanPolicy(c.Scan, ScanOverride{}).Allowlist; a.MaxDays != 90 || a.NewDays != 60 {
		t.Errorf("allowlist policy = %+v, want maxDays 90 newDays 60", a)
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
	if got := strings.Join(EffectiveScanPolicy(s, ScanOverride{}).FailOn, ","); got != "CRITICAL,HIGH" {
		t.Errorf("failOn default = %q", got)
	}
	p := EffectiveScanPolicy(s, ScanOverride{})
	if !p.IgnoreUnfixed {
		t.Error("ignoreUnfixed default should be true")
	}
	if p.Allowlist.MaxDays != 90 || p.Allowlist.NewDays != 90 || p.Allowlist.Generate || p.Allowlist.Preamble != "" {
		t.Errorf("allowlist defaults = %+v, want 90/90/false/empty", p.Allowlist)
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

func TestConfigResolveRejects(t *testing.T) {
	write := func(t *testing.T, content string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	registries := "registries: [{name: r, url: r.example.com/x}]\n"
	tests := []struct {
		name, content, wantErr string
	}{
		{name: "no registries", content: "signing: {provider: keyless}\n", wantErr: "at least one registry"},
		{name: "legacy singular registry block", content: "registry: {url: r.example.com/x}\n", wantErr: "registry"},
		{name: "missing registry name", content: "registries: [{url: r.example.com/x}]\n", wantErr: "name"},
		{
			name:    "bad registry name charset",
			content: "registries: [{name: \"Prod Registry\", url: r.example.com/x}]\n",
			wantErr: "name",
		},
		{name: "missing registry url", content: "registries: [{name: r}]\n", wantErr: "url is required"},
		{
			name:    "duplicate registry names",
			content: "registries: [{name: r, url: a.example.com/x}, {name: r, url: b.example.com/x}]\n",
			wantErr: "duplicate name",
		},
		{
			name:    "invalid per-registry signing",
			content: "registries: [{name: r, url: r.example.com/x, signing: {provider: kms}}]\n",
			wantErr: "kms.key",
		},
		{name: "unknown field", content: registries + "bogus: true\n", wantErr: "bogus"},
		{name: "kms without key", content: registries + "signing: {provider: kms}\n", wantErr: "kms.key"},
		{
			name:    "newDays beyond maxDays",
			content: registries + "scan: {allowlist: {maxDays: 30, newDays: 60}}\n",
			wantErr: "newDays (60) exceeds scan.allowlist.maxDays (30)",
		},
		{
			name:    "newDays beyond the default maxDays",
			content: registries + "scan: {allowlist: {newDays: 120}}\n",
			wantErr: "exceeds scan.allowlist.maxDays (90)",
		},
		{
			name:    "zero maxDays",
			content: registries + "scan: {allowlist: {maxDays: 0}}\n",
			wantErr: "maxDays must be positive",
		},
		{
			name:    "removed allowlistMaxDays key",
			content: registries + "scan: {allowlistMaxDays: 30}\n",
			wantErr: "allowlistMaxDays",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadConfig(t, write(t, tt.content))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("want error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestSelectRegistries(t *testing.T) {
	c, err := loadConfig(t, "testdata/config.yaml")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
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
	if g := m.Scan.Allowlist.Generate; g == nil || !*g {
		t.Errorf("scan.allowlist.generate = %v, want true", g)
	}
	if p := m.Scan.Allowlist.Preamble; p == nil || !strings.Contains(*p, "Whole-file") {
		t.Errorf("scan.allowlist.preamble = %v", p)
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
	if got := bundle.Artifact.Scan.Enabled.Effective(); got != ScanAuto {
		t.Errorf("scan enabled = %q", got)
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

func TestEffectiveScanPolicy(t *testing.T) {
	yes, no := true, false
	days := func(n int) *int { return &n }
	text := func(s string) *string { return &s }
	tests := []struct {
		name   string
		global Scan
		o      ScanOverride
		want   ScanPolicy
	}{
		{
			name: "built-in defaults",
			want: ScanPolicy{
				Enabled: true, Auto: true, FailOn: []string{"CRITICAL", "HIGH"}, IgnoreUnfixed: true,
				Allowlist: ResolvedAllowlist{MaxDays: 90, NewDays: 90},
			},
		},
		{
			name: "config overrides built-ins",
			global: Scan{
				FailOn: []string{"CRITICAL"}, IgnoreUnfixed: &no,
				Allowlist: AllowlistPolicy{MaxDays: days(60), NewDays: days(30), Generate: &yes, Preamble: text("g")},
			},
			want: ScanPolicy{
				Enabled: true, Auto: true, FailOn: []string{"CRITICAL"}, IgnoreUnfixed: false,
				Allowlist: ResolvedAllowlist{MaxDays: 60, NewDays: 30, Generate: true, Preamble: "g"},
			},
		},
		{
			name: "entry overrides config field by field",
			global: Scan{
				FailOn: []string{"CRITICAL"}, IgnoreUnfixed: &no,
				Allowlist: AllowlistPolicy{MaxDays: days(60), NewDays: days(30), Generate: &yes, Preamble: text("g")},
			},
			o: ScanOverride{
				FailOn: []string{"HIGH"}, IgnoreUnfixed: &yes,
				Allowlist: AllowlistPolicy{NewDays: days(10), Generate: &no, Preamble: text("")},
			},
			want: ScanPolicy{
				Enabled: true, Auto: true, FailOn: []string{"HIGH"}, IgnoreUnfixed: true,
				Allowlist: ResolvedAllowlist{MaxDays: 60, NewDays: 10, Generate: false, Preamble: ""},
			},
		},
		{
			name:   "auto defers to a disabled config",
			global: Scan{Enabled: &no},
			want: ScanPolicy{
				Enabled: false, Auto: true, FailOn: []string{"CRITICAL", "HIGH"}, IgnoreUnfixed: true,
				Allowlist: ResolvedAllowlist{MaxDays: 90, NewDays: 90},
			},
		},
		{
			name:   "explicit true overrides a disabled config",
			global: Scan{Enabled: &no},
			o:      ScanOverride{Enabled: ScanOn},
			want: ScanPolicy{
				Enabled: true, FailOn: []string{"CRITICAL", "HIGH"}, IgnoreUnfixed: true,
				Allowlist: ResolvedAllowlist{MaxDays: 90, NewDays: 90},
			},
		},
		{
			name:   "explicit false overrides an enabled config",
			global: Scan{Enabled: &yes},
			o:      ScanOverride{Enabled: ScanOff},
			want: ScanPolicy{
				Enabled: false, FailOn: []string{"CRITICAL", "HIGH"}, IgnoreUnfixed: true,
				Allowlist: ResolvedAllowlist{MaxDays: 90, NewDays: 90},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EffectiveScanPolicy(tt.global, tt.o)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("EffectiveScanPolicy =\n %+v\nwant\n %+v", got, tt.want)
			}
		})
	}
}

func TestResolvedAllowlistValidate(t *testing.T) {
	tests := []struct {
		a       ResolvedAllowlist
		wantErr string
	}{
		{a: ResolvedAllowlist{MaxDays: 90, NewDays: 90}},
		{a: ResolvedAllowlist{MaxDays: 30, NewDays: 60}, wantErr: "exceeds"},
		{a: ResolvedAllowlist{MaxDays: 0, NewDays: 0}, wantErr: "maxDays must be positive"},
		{a: ResolvedAllowlist{MaxDays: 30, NewDays: -1}, wantErr: "newDays must be positive"},
	}
	for _, tt := range tests {
		err := tt.a.Validate()
		if tt.wantErr == "" {
			if err != nil {
				t.Errorf("Validate(%+v) = %v", tt.a, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
			t.Errorf("Validate(%+v) = %v, want %q", tt.a, err, tt.wantErr)
		}
	}
}

func TestScanEnabledDecode(t *testing.T) {
	tests := []struct {
		in    string
		want  ScanEnabled
		valid bool
	}{
		{in: "enabled: true", want: ScanOn, valid: true},
		{in: "enabled: false", want: ScanOff, valid: true},
		{in: "enabled: auto", want: ScanAuto, valid: true},
		{in: `enabled: "true"`, want: ScanOn, valid: true},
		{in: "{}", want: "", valid: true},
		{in: "enabled: maybe", want: "maybe", valid: false},
	}
	for _, tt := range tests {
		var o ScanOverride
		if err := decodeStrict([]byte(tt.in), &o); err != nil {
			t.Errorf("decode %q: %v", tt.in, err)
			continue
		}
		if o.Enabled != tt.want || o.Enabled.Valid() != tt.valid {
			t.Errorf("decode %q = %q (valid %v), want %q (valid %v)", tt.in, o.Enabled, o.Enabled.Valid(), tt.want, tt.valid)
		}
	}
	var o ScanOverride
	if err := decodeStrict([]byte("enabled: [true]"), &o); err == nil {
		t.Error("a sequence decoded as scan.enabled")
	}
}

// TestManifestScanBlocks pins the one scan shape across both manifest
// kinds: the keys charts and artifacts already used still load, and each
// kind now accepts the other's keys too.
func TestManifestScanBlocks(t *testing.T) {
	dir := t.TempDir()
	chart := filepath.Join(dir, "chart.yaml")
	if err := os.WriteFile(chart, []byte("apiVersion: mirror.patchy.bitwisemedia.uk/v1alpha1\nkind: Chart\n"+
		"name: c\nscan:\n  enabled: false\n  allowlist: {maxDays: 30}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := LoadChartManifest(chart)
	if err != nil {
		t.Fatalf("LoadChartManifest: %v", err)
	}
	if m.Scan.Enabled != ScanOff || m.Scan.Allowlist.MaxDays == nil || *m.Scan.Allowlist.MaxDays != 30 {
		t.Errorf("chart scan = %+v", m.Scan)
	}
	artifact := filepath.Join(dir, "artifact.yaml")
	if err := os.WriteFile(artifact, []byte("apiVersion: mirror.patchy.bitwisemedia.uk/v1alpha1\nkind: Artifact\n"+
		"name: a\nscan:\n  enabled: true\n  failOn: [CRITICAL]\n  ignoreUnfixed: false\n"+
		"  allowlist: {generate: true}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := LoadArtifactManifest(artifact)
	if err != nil {
		t.Fatalf("LoadArtifactManifest: %v", err)
	}
	if a.Scan.Enabled != ScanOn || len(a.Scan.FailOn) != 1 || a.Scan.IgnoreUnfixed == nil ||
		a.Scan.Allowlist.Generate == nil || !*a.Scan.Allowlist.Generate {
		t.Errorf("artifact scan = %+v", a.Scan)
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
