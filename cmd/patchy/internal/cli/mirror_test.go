// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bitwise-media-group/patchy/internal/mirror"
	"github.com/bitwise-media-group/patchy/internal/mirror/scan"
)

// writeMirrorStore scaffolds a minimal store in the working directory.
func writeMirrorStore(t *testing.T) {
	t.Helper()
	if err := os.WriteFile("patchy.yaml", []byte(`registries:
  - name: primary
    url: registry.example.com/org/platform
`), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join("charts", "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `apiVersion: mirror.patchy.bitwisemedia.uk/v1alpha1
kind: Chart
name: demo
chart:
  repo: oci://ghcr.example.com/upstream
  name: demo
  version: "1.0.0"
images:
  verifyUpstream:
    - match: "**"
      provider: none
publish:
  chartRepo: charts/demo
`
	if err := os.WriteFile(filepath.Join(dir, "manifest.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMirrorHelpTree(t *testing.T) {
	t.Chdir(t.TempDir())
	out, err := execDev(t, "mirror", "--help")
	if err != nil {
		t.Fatalf("mirror --help: %v", err)
	}
	for _, sub := range []string{"add", "upgrade", "sync", "validate"} {
		if !strings.Contains(out, sub) {
			t.Errorf("help lacks %q:\n%s", sub, out)
		}
	}
}

func TestMirrorRequiresRegistries(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, args := range [][]string{
		{"mirror", "upgrade", "--all"},
		{"mirror", "sync", "--all"},
		{"mirror", "validate", "--all"},
	} {
		_, err := execDev(t, args...)
		if err == nil || !strings.Contains(err.Error(), "registries must list at least one registry") {
			t.Errorf("%v: error = %v, want the missing-registries refusal", args, err)
		}
	}
}

func TestMirrorSelectionValidation(t *testing.T) {
	t.Chdir(t.TempDir())
	writeMirrorStore(t)
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"all with names", []string{"mirror", "sync", "--all", "demo"}, "--all cannot be combined"},
		{"group with names", []string{"mirror", "upgrade", "--group", "g", "demo"}, "--group cannot be combined"},
		{"nothing selected", []string{"mirror", "sync"}, "name one or more entries"},
		{"unknown entry", []string{"mirror", "validate", "missing"}, "no entry"},
		{"to with all", []string{"mirror", "upgrade", "--all", "--to", "2.0.0"}, "--to needs named entries"},
		{"unknown stage", []string{"mirror", "validate", "--all", "--only", "bogus"}, "unknown stage"},
		{"unknown registry", []string{"mirror", "sync", "--all", "--registry", "nope"}, `unknown registry "nope"`},
		{"add without url", []string{"mirror", "add", "x"}, "--url is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := execDev(t, tt.args...)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want one containing %q", err, tt.want)
			}
		})
	}
}

func TestMirrorValidateLintOnly(t *testing.T) {
	// Lint is the one stage that runs without registries: exercise the
	// full command path end-to-end.
	t.Chdir(t.TempDir())
	writeMirrorStore(t)
	out, err := execDev(t, "mirror", "validate", "demo", "--only", "lint")
	if err != nil {
		t.Fatalf("validate --only lint: %v\n%s", err, out)
	}
	if !strings.Contains(out, "demo") || !strings.Contains(out, "ok") {
		t.Errorf("output:\n%s", out)
	}

	// A broken allowlist flips the exit and the row.
	if err := os.MkdirAll("charts/demo/security", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("charts/demo/security/allowlist.yaml",
		[]byte("vulnerabilities:\n  - id: CVE-1\n    statement: s\n    expired_at: \"2020-01-01\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err = execDev(t, "mirror", "validate", "demo", "--only", "lint")
	if err == nil || !strings.Contains(err.Error(), "failed validation") {
		t.Fatalf("error = %v\n%s", err, out)
	}
}

func TestMirrorValidateMarkdown(t *testing.T) {
	// End-to-end through the command: a clean entry and a failing one.
	t.Chdir(t.TempDir())
	writeMirrorStore(t)
	out, err := execDev(t, "mirror", "validate", "demo", "--only", "lint", "-o", "markdown")
	if err != nil {
		t.Fatalf("validate -o markdown: %v\n%s", err, out)
	}
	if !strings.Contains(out, "### `demo`") || !strings.Contains(out, "clean ✔") {
		t.Errorf("markdown output:\n%s", out)
	}

	if err := os.MkdirAll("charts/demo/security", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("charts/demo/security/allowlist.yaml",
		[]byte("vulnerabilities:\n  - id: CVE-1\n    statement: s\n    expired_at: \"2020-01-01\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err = execDev(t, "mirror", "validate", "demo", "--only", "lint", "-o", "markdown")
	if err == nil {
		t.Fatal("want validation failure")
	}
	if !strings.Contains(out, "**lint:**") || !strings.Contains(out, "CVE-1 expired on 2020-01-01") ||
		strings.Contains(out, "clean ✔") {
		t.Errorf("failing markdown output:\n%s", out)
	}
}

func TestRenderValidateMarkdownSections(t *testing.T) {
	out := &bytes.Buffer{}
	opts := &Options{Out: out, ErrOut: &bytes.Buffer{}}
	err := renderValidateMarkdown(opts, []mirror.ValidateResult{
		{
			Name:       "stale",
			Kind:       "Chart",
			RegenDiffs: []string{"images.lock.yaml differs from a fresh discovery"},
			Scan: &mirror.ScanReport{
				Blocking: []scan.Finding{{
					ID: "CVE-2026-1", Severity: "CRITICAL",
					Package: "libfoo", Installed: "1.0.0", FixedIn: []string{"1.0.1"},
				}},
				Suppressed: 2,
			},
			Verify: &mirror.VerifyReport{Gaps: []string{"chart stale (no upstream provenance declared)"}},
		},
		{Name: "broken", Kind: "Chart", Err: "boom"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"### `stale`",
		"**stale derived state** (run upgrade):",
		"```\nimages.lock.yaml differs from a fresh discovery\n```",
		"CVE-2026-1 CRITICAL — libfoo 1.0.0 (fixed in 1.0.1)",
		"2 finding(s) suppressed by the allowlist.",
		"Provenance gaps (documented): chart stale (no upstream provenance declared)",
		"### `broken`",
		"**error:** boom",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("markdown lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "clean ✔") {
		t.Errorf("problem entries must not read clean:\n%s", got)
	}
}

func TestRenderSyncMarkdown(t *testing.T) {
	out := &bytes.Buffer{}
	opts := &Options{Out: out, ErrOut: &bytes.Buffer{}}
	err := renderSyncMarkdown(opts, []mirror.SyncResult{
		{
			Name: "demo",
			Kind: "Chart",
			Records: []mirror.SyncRecord{
				{Registry: "primary", Ref: "reg.example.com/charts/demo:1.0.0", Action: mirror.ActionPushed, Signed: true},
				{Registry: "ghcr", Ref: "ghcr.io/org/images/x:1.0.0", Action: mirror.ActionSkippedCurrent},
			},
		},
		{Name: "broken", Kind: "Artifact", Err: "digest mismatch"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"### `demo`",
		"- `pushed` [primary] reg.example.com/charts/demo:1.0.0 (signed)",
		"- `skipped-current` [ghcr] ghcr.io/org/images/x:1.0.0",
		"### `broken`",
		"**error:** digest mismatch",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("markdown lacks %q:\n%s", want, got)
		}
	}
}

// TestMirrorDirectoryFlagAndEnv pins the store root: -C, the environment
// and mirror.directory resolve relative to the working directory, while
// the patchy config is still found from the working directory.
func TestMirrorDirectoryFlagAndEnv(t *testing.T) {
	base := t.TempDir()
	store := filepath.Join(base, "store")
	if err := os.MkdirAll(store, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(store)
	writeMirrorStore(t)
	if err := os.Rename(filepath.Join(store, "patchy.yaml"), filepath.Join(base, "patchy.yaml")); err != nil {
		t.Fatal(err)
	}
	t.Chdir(base)

	if _, err := execDev(t, "mirror", "validate", "demo", "--only", "lint", "-C", "store"); err != nil {
		t.Fatalf("-C: %v", err)
	}
	if _, err := execDev(t, "mirror", "validate", "demo", "--only", "lint", "-C", "absent"); err == nil ||
		!strings.Contains(err.Error(), "does not exist") {
		t.Errorf("-C absent: %v, want the missing-directory refusal", err)
	}
	t.Setenv("PATCHY_MIRROR_DIRECTORY", store)
	if _, err := execDev(t, "mirror", "validate", "demo", "--only", "lint"); err != nil {
		t.Fatalf("env directory: %v", err)
	}
	t.Setenv("PATCHY_MIRROR_DIRECTORY", "")
	raw, err := os.ReadFile("patchy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("patchy.yaml", append(raw, "mirror:\n  directory: store\n"...), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := execDev(t, "mirror", "validate", "demo", "--only", "lint"); err != nil {
		t.Fatalf("mirror.directory: %v", err)
	}
}

func TestMirrorAddScaffoldsArtifact(t *testing.T) {
	t.Chdir(t.TempDir())
	writeMirrorStore(t)
	// A bare ref with an explicit version needs no registry access.
	out, err := execDev(t, "mirror", "add", "bundle",
		"--url", "ghcr.example.com/org/bundle", "--type", "artifact", "--version", "1.2.3")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if strings.TrimSpace(out) != "bundle" {
		t.Errorf("stdout = %q", out)
	}
	raw, err := os.ReadFile("artifacts/bundle/manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"kind: Artifact", "ref: ghcr.example.com/org/bundle",
		`version: "1.2.3"`, `versionConstraint: ">=1.2.3 <2.0.0"`,
	} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("manifest lacks %q:\n%s", want, raw)
		}
	}
	// The scaffold must load through the strict parser.
	if _, err := execDev(t, "mirror", "validate", "bundle", "--only", "lint"); err != nil {
		t.Fatalf("scaffold fails lint: %v", err)
	}
	// Adding again refuses to clobber.
	if _, err := execDev(t, "mirror", "add", "bundle",
		"--url", "ghcr.example.com/org/bundle", "--type", "artifact", "--version", "1.2.3"); err == nil {
		t.Error("want already-exists error")
	}
}

func TestMirrorAddInitializesStore(t *testing.T) {
	t.Chdir(t.TempDir())
	if _, err := execDev(t, "mirror", "add", "bundle",
		"--url", "ghcr.example.com/org/bundle", "--type", "artifact", "--version", "1.2.3"); err != nil {
		t.Fatalf("add on empty dir: %v", err)
	}
	raw, err := os.ReadFile("patchy.yaml")
	if err != nil {
		t.Fatalf("patchy.yaml not scaffolded: %v", err)
	}
	if !strings.Contains(string(raw), "registries:") || !strings.Contains(string(raw), "mirror:") {
		t.Errorf("patchy.yaml:\n%s", raw)
	}
	if _, err := os.Stat("mirror.yaml"); err == nil {
		t.Error("add scaffolded the removed mirror.yaml")
	}
	// A second add finds the project config and leaves it alone.
	before := string(raw)
	if _, err := execDev(t, "mirror", "add", "other",
		"--url", "ghcr.example.com/org/other", "--type", "artifact", "--version", "1.0.0"); err != nil {
		t.Fatalf("second add: %v", err)
	}
	if after, _ := os.ReadFile("patchy.yaml"); string(after) != before {
		t.Error("a second add rewrote patchy.yaml")
	}
}
