// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"

	"github.com/bitwise-media-group/patchy/internal/build"
	"github.com/bitwise-media-group/patchy/internal/build/buildx"
	"github.com/bitwise-media-group/patchy/internal/mirror/scan"
	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
)

// buildConfig is a project patchy.yaml with one image and one manifests
// entry, registries pointing nowhere reachable (dry runs never call them).
const buildConfig = `registries:
  - name: primary
    url: registry.invalid/org
  - name: ghcr
    url: ghcr.invalid/org
build:
  architectures: [amd64]
  tags: ["${VERSION}-edge.${PATCHY_TIMESTAMP}", "sha-${GITHUB_SHA}"]
  images:
    - name: controller
      repository: patchy/controller
      scan:
        allowlist: {generate: true}
  manifests:
    - name: platform
      repository: manifests/platform
      path: deploy/flux
`

// cliBuilder writes a random linux/amd64 image into the layout.
type cliBuilder struct{ calls int }

func (b *cliBuilder) Build(_ context.Context, r buildx.Request) error {
	b.calls++
	img, err := random.Image(64, 1)
	if err != nil {
		return err
	}
	cfg, err := img.ConfigFile()
	if err != nil {
		return err
	}
	cfg = cfg.DeepCopy()
	cfg.OS, cfg.Architecture = "linux", "amd64"
	if img, err = mutate.ConfigFile(img, cfg); err != nil {
		return err
	}
	p, err := layout.Write(r.Output, empty.Index)
	if err != nil {
		return err
	}
	return p.AppendImage(img)
}

// cliScanner reports canned findings.
type cliScanner struct{ findings []scan.Finding }

func (s *cliScanner) Name() string { return "fake" }
func (s *cliScanner) ScanArchive(context.Context, string) ([]scan.Finding, error) {
	return s.findings, nil
}

// cveOne is the blocking finding the gate tests report.
var cveOne = scan.Finding{ID: "CVE-1", Severity: "CRITICAL", Package: "libfoo", Installed: "1", FixedIn: []string{"2"}}

// cliTools reports every binary missing (kubescape skips).
type cliTools struct{}

func (cliTools) Run(context.Context, string, []string, []string) ([]byte, error) {
	return nil, errors.New("unexpected tool run")
}
func (cliTools) Look(string) bool { return false }

// buildWorkspace writes the config and manifests tree into a fresh working
// directory and injects the fakes, returning the builder and scanner.
func buildWorkspace(t *testing.T, config string) (*cliBuilder, *cliScanner) {
	t.Helper()
	t.Chdir(t.TempDir())
	if err := os.WriteFile("patchy.yaml", []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join("deploy", "flux"), 0o755); err != nil {
		t.Fatal(err)
	}
	kustomization := filepath.Join("deploy", "flux", "kustomization.yaml")
	if err := os.WriteFile(kustomization, []byte("resources: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b, s := &cliBuilder{}, &cliScanner{}
	buildEngineHook = func(c *build.Config) {
		c.Builder = b
		c.ArchiveScanners = []scan.ArchiveScanner{s}
		c.Tools = cliTools{}
		c.Now = func() time.Time { return time.Date(2026, 11, 10, 9, 8, 7, 0, time.UTC) }
		c.LookupEnv = func(k string) (string, bool) {
			v, ok := map[string]string{"VERSION": "1.4.0", "GITHUB_SHA": "abc1234"}[k]
			return v, ok
		}
	}
	t.Cleanup(func() { buildEngineHook = nil })
	return b, s
}

func TestBuildCommandValidation(t *testing.T) {
	tests := []struct {
		name, config string
		args         []string
		want         string
		usage        bool
	}{
		{name: "no selection", config: buildConfig, args: []string{"build"}, want: "pass --all", usage: true},
		{
			name: "all plus names", config: buildConfig,
			args: []string{"build", "--all", "controller"}, want: "cannot be combined", usage: true,
		},
		{
			name: "unknown entry", config: buildConfig,
			args: []string{"build", "nope"}, want: `no build entry "nope"`, usage: true,
		},
		{
			name: "unknown registry", config: buildConfig,
			args: []string{"build", "--all", "--registry", "nope"}, want: `unknown registry "nope"`, usage: true,
		},
		{
			name: "exclusive modes", config: buildConfig,
			args: []string{"build", "--all", "--dry-run", "--update-allowlists"}, want: "none of the others can be",
		},
		{name: "no registries", config: "build: {}\n", args: []string{"build", "--all"}, want: "registries must list"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buildWorkspace(t, tt.config)
			_, err := execDev(t, tt.args...)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
			if tt.usage && !isUsage(err) {
				t.Errorf("not a usage error: %v", err)
			}
		})
	}
}

// TestBuildCommandNoEntries: --all over an empty build: block is a narrated
// no-op, so a shared task can run it in a repo that only mirrors.
func TestBuildCommandNoEntries(t *testing.T) {
	buildWorkspace(t, "registries: [{name: r, url: r.invalid/x}]\n")
	for _, args := range [][]string{
		{"build", "--all", "--dry-run", "-o", "json"},
		{"build", "--all", "--update-allowlists", "-o", "json"},
	} {
		out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
		root := NewRoot(&Options{Out: out, ErrOut: errOut, Output: "table", NoColor: true})
		root.SetOut(out)
		root.SetErr(errOut)
		root.SetArgs(args)
		if err := root.ExecuteContext(context.Background()); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if strings.TrimSpace(out.String()) != "[]" {
			t.Errorf("%v stdout = %q, want []", args, out)
		}
		if !strings.Contains(errOut.String(), "no build entries") {
			t.Errorf("%v stderr = %q, want the no-entries note", args, errOut)
		}
	}
}

// TestBuildCommandDryRun drives the whole command with fakes: every target
// reported as would-push, nothing contacted (the registries are .invalid).
func TestBuildCommandDryRun(t *testing.T) {
	b, _ := buildWorkspace(t, buildConfig)
	out, err := execDev(t, "build", "--all", "--dry-run", "-o", "json", "--registry", "ghcr")
	if err != nil {
		t.Fatalf("build --dry-run: %v\n%s", err, out)
	}
	var results []build.Result
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		t.Fatalf("decode %s: %v", out, err)
	}
	if len(results) != 2 || results[0].Name != "controller" || results[1].Name != "platform" {
		t.Fatalf("results = %+v", results)
	}
	for _, r := range results {
		if len(r.Records) != 2 || r.Records[0].Action != build.ActionWouldPush ||
			r.Records[0].Ref != "ghcr.invalid/org/"+map[string]string{
				"controller": "patchy/controller", "platform": "manifests/platform",
			}[r.Name]+":1.4.0-edge.20261110090807" {
			t.Errorf("%s records = %+v", r.Name, r.Records)
		}
	}
	if results[0].Scan == nil || len(results[0].Scan.Scanned) != 1 || b.calls != 1 {
		t.Errorf("controller scan = %+v, builds %d", results[0].Scan, b.calls)
	}

	// The table and markdown renderings of the same run.
	out, err = execDev(t, "build", "controller", "--dry-run", "--tag", "v${VERSION}")
	if err != nil || !strings.Contains(out, "registry.invalid/org/patchy/controller:v1.4.0") ||
		!strings.Contains(out, "would-push") {
		t.Errorf("table = %v\n%s", err, out)
	}
	out, err = execDev(t, "build", "controller", "--dry-run", "-o", "markdown")
	if err != nil || !strings.Contains(out, "### `controller`") ||
		!strings.Contains(out, "Scanned linux/amd64: 0 blocking") {
		t.Errorf("markdown = %v\n%s", err, out)
	}
}

func TestBuildCommandBlockingFinding(t *testing.T) {
	_, s := buildWorkspace(t, buildConfig)
	s.findings = []scan.Finding{cveOne}
	out, err := execDev(t, "build", "--all", "--dry-run", "-o", "markdown")
	if err == nil || !strings.Contains(err.Error(), "1 entry failed to build") {
		t.Fatalf("error = %v, want the summary failure", err)
	}
	// --all continued past the failure to the manifests entry.
	for _, want := range []string{
		"**error:** 1 blocking finding(s); nothing pushed", "CVE-1 CRITICAL libfoo 1 (fixed in 2)",
		"### `platform`", "would-push",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("markdown lacks %q:\n%s", want, out)
		}
	}
}

func TestBuildCommandUpdateAllowlists(t *testing.T) {
	_, s := buildWorkspace(t, buildConfig)
	s.findings = []scan.Finding{cveOne}
	out, err := execDev(t, "build", "--all", "--update-allowlists")
	if err != nil {
		t.Fatalf("--update-allowlists: %v\n%s", err, out)
	}
	for _, want := range []string{
		filepath.Join("security", "allowlists", "controller.yaml"), "0 kept, 0 renewed, 1 new, 0 dropped",
		"skipped: manifests entries have no allowlist",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	allow, err := spec.LoadAllowlistFile(filepath.Join("security", "allowlists", "controller.yaml"))
	if err != nil || len(allow.Vulnerabilities) != 1 || allow.Vulnerabilities[0].ID != "CVE-1" {
		t.Errorf("allowlist = %+v, %v", allow, err)
	}
}

func TestRenderBuildResultsTable(t *testing.T) {
	var out bytes.Buffer
	opts := &Options{Out: &out}
	err := renderBuildResults(opts, []build.Result{
		{Name: "controller", Records: []build.Record{
			{Registry: "ghcr", Ref: "ghcr.io/org/c:v1", Action: build.ActionPushed, Signed: true},
			{Registry: "ghcr", Ref: "ghcr.io/org/c:sha-1", Action: build.ActionSkippedTagExists},
		}},
		{Name: "broken", Err: "boom"},
	}, "table")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ENTRY", "ghcr.io/org/c:v1", "pushed", "signed", "skipped-tag-exists", "failed: boom"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("table lacks %q:\n%s", want, out.String())
		}
	}
}
