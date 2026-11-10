// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package fluxartifact

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
)

// writeTree creates files (path → content; a trailing / makes a dir).
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		path := filepath.Join(root, rel)
		if strings.HasSuffix(rel, "/") {
			if err := os.MkdirAll(path, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
			t.Fatal(err)
		}
	}
}

var tree = map[string]string{
	"kustomization.yaml":          "resources: [apps]\n",
	"apps/app.yaml":               "kind: Deployment\n",
	"apps/README.md":              "docs\n",
	"tests/case.yaml":             "kind: Test\n",
	"scripts/render.sh":           "#!/bin/sh\n",
	".git/HEAD":                   "ref: refs/heads/main\n",
	"nested/tests/keep.yaml":      "kind: Kept\n",
	"nested/deep/empty-dir-kept/": "",
}

// stage stages tree under ignore and returns the staged dir.
func stage(t *testing.T, files map[string]string, ignore []string, mtime time.Time) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "src")
	writeTree(t, src, files)
	if err := os.Chmod(filepath.Join(src, "scripts", "render.sh"), 0o700); err != nil && files["scripts/render.sh"] != "" {
		t.Fatal(err)
	}
	if !mtime.IsZero() {
		_ = filepath.WalkDir(src, func(p string, _ os.DirEntry, _ error) error {
			return os.Chtimes(p, mtime, mtime)
		})
	}
	dst := filepath.Join(t.TempDir(), "staged")
	if err := Stage(src, dst, ignore); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	return dst
}

// listTar returns "name mode" per entry of a tar.gz, plus header checks.
func listTar(t *testing.T, tgz []byte) []string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(tgz))
	if err != nil {
		t.Fatal(err)
	}
	if gz.Name != "" || !gz.ModTime.IsZero() {
		t.Errorf("gzip header name %q mtime %v, want neither", gz.Name, gz.ModTime)
	}
	tr := tar.NewReader(gz)
	var out []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.ModTime.Unix() != 0 || h.Uid != 0 || h.Gid != 0 || h.Uname != "" || h.Gname != "" {
			t.Errorf("%s: header not normalized: %+v", h.Name, h)
		}
		out = append(out, h.Name+" "+os.FileMode(h.Mode).String())
	}
	return out
}

func TestStageAndArchive(t *testing.T) {
	staged := stage(t, tree, []string{"*.md", "/tests/"}, time.Time{})
	tgz, err := Archive(staged)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"apps/ -rwxr-xr-x",
		"apps/app.yaml -rw-r--r--",
		"kustomization.yaml -rw-r--r--",
		"nested/ -rwxr-xr-x",
		"nested/deep/ -rwxr-xr-x",
		"nested/deep/empty-dir-kept/ -rwxr-xr-x",
		"nested/tests/ -rwxr-xr-x",
		"nested/tests/keep.yaml -rw-r--r--",
		"scripts/ -rwxr-xr-x",
		"scripts/render.sh -rwxr-xr-x",
	}
	if got := listTar(t, tgz); !reflect.DeepEqual(got, want) {
		t.Errorf("archive =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestDigestIgnoresMtime pins content addressing: the same tree staged at
// different times packages to the same digest; a content change moves it.
func TestDigestIgnoresMtime(t *testing.T) {
	ann := map[string]string{SourceAnnotation: "https://github.com/org/repo", RevisionAnnotation: "main@sha1:abc"}
	digest := func(files map[string]string, mtime time.Time) v1.Hash {
		img, err := Package(stage(t, files, nil, mtime), ann)
		if err != nil {
			t.Fatal(err)
		}
		d, err := img.Digest()
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	a := digest(tree, time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
	b := digest(tree, time.Date(2026, 6, 6, 6, 6, 6, 0, time.UTC))
	if a != b {
		t.Errorf("digest moved with mtime: %s vs %s", a, b)
	}
	changed := map[string]string{"kustomization.yaml": "resources: []\n"}
	if c := digest(changed, time.Time{}); c == a {
		t.Error("digest ignored a content change")
	}
}

func TestPackageShape(t *testing.T) {
	img, err := Package(stage(t, tree, nil, time.Time{}), map[string]string{
		SourceAnnotation: "https://github.com/org/repo", RevisionAnnotation: "main@sha1:abc", "empty": "",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := img.RawManifest()
	if err != nil {
		t.Fatal(err)
	}
	var m v1.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m.MediaType != "application/vnd.oci.image.manifest.v1+json" || m.Config.MediaType != ConfigMediaType ||
		len(m.Layers) != 1 || m.Layers[0].MediaType != ContentMediaType {
		t.Errorf("manifest = %s", raw)
	}
	wantAnn := map[string]string{SourceAnnotation: "https://github.com/org/repo", RevisionAnnotation: "main@sha1:abc"}
	if !reflect.DeepEqual(m.Annotations, wantAnn) {
		t.Errorf("annotations = %v (no created, no empty values)", m.Annotations)
	}
	cfg, err := img.RawConfigFile()
	if err != nil || string(cfg) != "{}" {
		t.Errorf("config = %q, %v", cfg, err)
	}
	layers, err := img.Layers()
	if err != nil || len(layers) != 1 {
		t.Fatalf("layers = %v, %v", layers, err)
	}
	rc, err := layers[0].Compressed()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()
	tgz, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if entries := listTar(t, tgz); len(entries) == 0 || !strings.HasPrefix(entries[0], "apps/") {
		t.Errorf("layer entries = %v", entries)
	}
}

func TestStageRejects(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		src := t.TempDir()
		writeTree(t, src, map[string]string{"a.yaml": "x"})
		if err := os.Symlink("/etc/passwd", filepath.Join(src, "leak.yaml")); err != nil {
			t.Fatal(err)
		}
		err := Stage(src, filepath.Join(t.TempDir(), "out"), nil)
		if err == nil || !strings.Contains(err.Error(), "leak.yaml is a symlink") {
			t.Errorf("Stage = %v, want the symlink refusal", err)
		}
	})
	t.Run("ignored symlink is fine", func(t *testing.T) {
		src := t.TempDir()
		writeTree(t, src, map[string]string{"a.yaml": "x"})
		if err := os.Symlink("/etc/passwd", filepath.Join(src, "leak.md")); err != nil {
			t.Fatal(err)
		}
		if err := Stage(src, filepath.Join(t.TempDir(), "out"), []string{"*.md"}); err != nil {
			t.Errorf("Stage = %v", err)
		}
	})
	t.Run("not a directory", func(t *testing.T) {
		src := filepath.Join(t.TempDir(), "file")
		writeTree(t, filepath.Dir(src), map[string]string{"file": "x"})
		if err := Stage(src, filepath.Join(t.TempDir(), "out"), nil); err == nil {
			t.Error("Stage accepted a file")
		}
	})
}
