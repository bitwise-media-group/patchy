// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/bitwise-media-group/patchy/internal/mirror/helmchart"
	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
)

func TestDetectAddTarget(t *testing.T) {
	// Rows never reach the network: the auto-detect oci:// arm is covered
	// by TestMirrorAddDetectsFromRegistry.
	tests := []struct {
		name         string
		spec         addSpec
		wantKind     string
		wantRepo     string
		wantChart    string
		wantArtifact string
		wantErr      string
	}{
		{
			name:    "https repo cannot be an artifact",
			spec:    addSpec{url: "https://charts.example.test", entryType: "artifact"},
			wantErr: "artifacts need a registry reference",
		},
		{
			name:    "https repo needs the entry name",
			spec:    addSpec{url: "https://charts.example.test"},
			wantErr: "pass the entry name",
		},
		{
			name:      "https repo names the chart after the entry",
			spec:      addSpec{name: "dex", url: "https://charts.example.test"},
			wantKind:  spec.KindChart,
			wantRepo:  "https://charts.example.test",
			wantChart: "dex",
		},
		{
			name:      "oci forced chart splits repo and name",
			spec:      addSpec{url: "oci://ghcr.example.test/org/charts/dex", entryType: "chart"},
			wantKind:  spec.KindChart,
			wantRepo:  "oci://ghcr.example.test/org/charts",
			wantChart: "dex",
		},
		{
			name:         "oci forced artifact keeps the whole path",
			spec:         addSpec{url: "oci://ghcr.example.test/org/bundle", entryType: "artifact"},
			wantKind:     spec.KindArtifact,
			wantArtifact: "ghcr.example.test/org/bundle",
		},
		{
			name:    "oci unknown type",
			spec:    addSpec{url: "oci://ghcr.example.test/org/bundle", entryType: "image"},
			wantErr: `unknown --type "image"`,
		},
		{
			name:    "bare ref cannot be a chart",
			spec:    addSpec{url: "ghcr.example.test/org/bundle", entryType: "chart"},
			wantErr: "a chart needs an oci:// or https:// URL",
		},
		{
			name:         "bare ref defaults to artifact",
			spec:         addSpec{url: "ghcr.example.test/org/bundle"},
			wantKind:     spec.KindArtifact,
			wantArtifact: "ghcr.example.test/org/bundle",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, repo, chartName, artifactRef, err := detectAddTarget(context.Background(), tt.spec)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want one containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("detectAddTarget: %v", err)
			}
			if kind != tt.wantKind || repo != tt.wantRepo || chartName != tt.wantChart || artifactRef != tt.wantArtifact {
				t.Errorf("= (%q, %q, %q, %q), want (%q, %q, %q, %q)",
					kind, repo, chartName, artifactRef,
					tt.wantKind, tt.wantRepo, tt.wantChart, tt.wantArtifact)
			}
		})
	}
}

func TestStayInMajor(t *testing.T) {
	tests := []struct {
		version string
		want    string
		wantErr bool
	}{
		{"1.2.3", ">=1.2.3 <2.0.0", false},
		// A v-prefixed pin must produce the same constraint — both halves
		// of the range are prefix-free.
		{"v1.2.3", ">=1.2.3 <2.0.0", false},
		{"0.5.0", ">=0.5.0 <1.0.0", false},
		{"not-a-version", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			got, err := stayInMajor(tt.version)
			if (err != nil) != tt.wantErr {
				t.Fatalf("stayInMajor(%q) error = %v, wantErr %v", tt.version, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("stayInMajor(%q) = %q, want %q", tt.version, got, tt.want)
			}
		})
	}
}

// TestScaffoldsSurviveTheStrictParser round-trips every scaffold through
// the same strict loaders the pipeline uses: a scaffold the parser rejects
// bricks the store it just created.
func TestScaffoldsSurviveTheStrictParser(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "mirror.yaml")
	if err := os.WriteFile(configPath, scaffoldMirrorConfig(), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := spec.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("scaffolded mirror.yaml does not parse: %v", err)
	}
	if !cfg.Scan.Scanners.OSVEnabled() || cfg.Scan.Scanners.GrypeEnabled() || !cfg.Scan.Scanners.KubescapeEnabled() {
		t.Errorf("scanner toggles = %+v", cfg.Scan.Scanners)
	}
	if cfg.Update.EffectiveCooldownDays() != 3 || cfg.Scan.AllowlistMaxDays != 90 {
		t.Errorf("config = update %d, allowlistMaxDays %d", cfg.Update.EffectiveCooldownDays(), cfg.Scan.AllowlistMaxDays)
	}

	chartPath := filepath.Join(dir, "chart-manifest.yaml")
	chartScaffold := scaffoldChartManifest("demo", "oci://reg.example.test/upstream", "demo",
		"1.2.3", ">=1.2.3 <2.0.0", "grp")
	if err := os.WriteFile(chartPath, chartScaffold, 0o644); err != nil {
		t.Fatal(err)
	}
	cm, err := spec.LoadChartManifest(chartPath)
	if err != nil {
		t.Fatalf("scaffolded chart manifest does not parse: %v", err)
	}
	// The default publish path must come from the engine, not the
	// scaffold: a hardcoded publish block would silently defeat a store's
	// configured namespaces.
	if cm.Publish.ChartRepo != "" {
		t.Errorf("chart scaffold pins publish.chartRepo = %q", cm.Publish.ChartRepo)
	}
	if cm.Chart.Lockstep != "grp" || cm.Chart.Version != "1.2.3" {
		t.Errorf("chart manifest = %+v", cm.Chart)
	}

	artifactPath := filepath.Join(dir, "artifact-manifest.yaml")
	artifactScaffold := scaffoldArtifactManifest("bundle", "ghcr.example.test/org/bundle",
		"1.2.3", ">=1.2.3 <2.0.0", "")
	if err := os.WriteFile(artifactPath, artifactScaffold, 0o644); err != nil {
		t.Fatal(err)
	}
	am, err := spec.LoadArtifactManifest(artifactPath)
	if err != nil {
		t.Fatalf("scaffolded artifact manifest does not parse: %v", err)
	}
	if am.Publish.Repo != "" {
		t.Errorf("artifact scaffold pins publish.repo = %q", am.Publish.Repo)
	}
	if am.Scan.EffectiveEnabled() != "auto" {
		t.Errorf("artifact scan = %+v", am.Scan)
	}
}

// TestMirrorAddBrokenConfigPropagates pins the init gate: only the
// no-store miss may scaffold a fresh mirror.yaml — a broken CLI config
// must surface as the error it is, not be papered over with a new store.
func TestMirrorAddBrokenConfigPropagates(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile(".patchy.yaml", []byte("mirror: [broken\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := execDev(t, "mirror", "add", "bundle",
		"--url", "ghcr.example.test/org/bundle", "--type", "artifact", "--version", "1.2.3")
	if err == nil || !strings.Contains(err.Error(), ".patchy.yaml") {
		t.Fatalf("error = %v, want the config parse failure", err)
	}
	if _, statErr := os.Stat("mirror.yaml"); statErr == nil {
		t.Error("a broken config scaffolded a store anyway")
	}
}

// addTestRegistry stands up an in-memory registry with a chart repository
// (semver tags, helm config media type), a plain image, and an untagged
// bundle, returning its host.
func addTestRegistry(t *testing.T) string {
	t.Helper()
	s := httptest.NewServer(registry.New())
	t.Cleanup(s.Close)
	u, err := url.Parse(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	host := u.Host

	pushChart := func(ref string) {
		t.Helper()
		layer := static.NewLayer([]byte("tgz-bytes"), types.MediaType(helmchart.ChartContentLayerMediaType))
		img := mutate.MediaType(empty.Image, types.OCIManifestSchema1)
		img = mutate.ConfigMediaType(img, types.MediaType(helmchart.ChartConfigMediaType))
		img, err := mutate.AppendLayers(img, layer)
		if err != nil {
			t.Fatal(err)
		}
		if err := crane.Push(img, ref); err != nil {
			t.Fatal(err)
		}
	}
	pushImage := func(ref string) {
		t.Helper()
		img, err := random.Image(64, 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := crane.Push(img, ref); err != nil {
			t.Fatal(err)
		}
	}
	pushChart(host + "/upstream/otel:1.1.0")
	pushChart(host + "/upstream/otel:1.2.0")
	pushChart(host + "/upstream/otel:1.3.0-rc.1") // pre-release: never auto-pinned
	pushImage(host + "/org/thing:2.0.0")
	pushImage(host + "/org/latest-only:latest")
	return host
}

// TestMirrorAddDetectsFromRegistry drives the flag-free path a user hits
// first: media-type auto-detection, name defaulting, newest-stable
// pinning, and scaffolds that survive the strict parser and lint.
func TestMirrorAddDetectsFromRegistry(t *testing.T) {
	host := addTestRegistry(t)
	t.Chdir(t.TempDir())
	writeMirrorStore(t)

	t.Run("chart repository", func(t *testing.T) {
		out, err := execDev(t, "mirror", "add", "--url", fmt.Sprintf("oci://%s/upstream/otel", host))
		if err != nil {
			t.Fatalf("add: %v", err)
		}
		// The name defaults to the chart, the pin to the newest stable
		// release (1.3.0-rc.1 must lose to 1.2.0).
		if strings.TrimSpace(out) != "otel" {
			t.Errorf("stdout = %q", out)
		}
		raw, err := os.ReadFile("charts/otel/manifest.yaml")
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{
			"kind: Chart",
			fmt.Sprintf("repo: oci://%s/upstream", host),
			"name: otel",
			`version: "1.2.0"`,
			`versionConstraint: ">=1.2.0 <2.0.0"`,
		} {
			if !strings.Contains(string(raw), want) {
				t.Errorf("manifest lacks %q:\n%s", want, raw)
			}
		}
		if _, err := os.Stat(filepath.Join("charts", "otel", "values", "discovery.yaml")); err != nil {
			t.Errorf("discovery values not scaffolded: %v", err)
		}
		if _, err := execDev(t, "mirror", "validate", "otel", "--only", "lint"); err != nil {
			t.Errorf("chart scaffold fails lint: %v", err)
		}
	})

	t.Run("plain image", func(t *testing.T) {
		out, err := execDev(t, "mirror", "add", "--url", fmt.Sprintf("oci://%s/org/thing", host))
		if err != nil {
			t.Fatalf("add: %v", err)
		}
		if strings.TrimSpace(out) != "thing" {
			t.Errorf("stdout = %q", out)
		}
		raw, err := os.ReadFile("artifacts/thing/manifest.yaml")
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{
			"kind: Artifact",
			fmt.Sprintf("ref: %s/org/thing", host),
			`version: "2.0.0"`,
		} {
			if !strings.Contains(string(raw), want) {
				t.Errorf("manifest lacks %q:\n%s", want, raw)
			}
		}
		if _, err := execDev(t, "mirror", "validate", "thing", "--only", "lint"); err != nil {
			t.Errorf("artifact scaffold fails lint: %v", err)
		}
	})

	t.Run("no release tags refuses auto-detection", func(t *testing.T) {
		_, err := execDev(t, "mirror", "add", "--url", fmt.Sprintf("oci://%s/org/latest-only", host))
		if err == nil || !strings.Contains(err.Error(), "no release tags") {
			t.Fatalf("error = %v, want the no-release-tags refusal", err)
		}
	})
}
