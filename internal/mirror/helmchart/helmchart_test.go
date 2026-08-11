// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package helmchart

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
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
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/bitwise-media-group/patchy/internal/mirror/ocireg"
)

// makeTgz builds a tgz from name->content pairs.
func makeTgz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, name := range sortedKeys(files) {
		content := files[name]
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := range keys {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	return keys
}

var demoChartFiles = map[string]string{
	"demo/Chart.yaml":          "apiVersion: v2\nname: demo\nversion: 1.0.0\nappVersion: v2.5.0\n",
	"demo/values.yaml":         "image: ghcr.io/example/app\n",
	"demo/templates/cm.yaml":   "kind: ConfigMap\n",
	"demo/templates/_helpers":  "{{/* helpers */}}\n",
	"demo/charts/sub/x.yaml":   "nested: true\n",
	"demo/.helmignore":         "*.bak\n",
	"demo/files/binary.txt":    "\x00\x01\x02",
	"demo/templates/note.txt":  "note\n",
	"demo/templates/svc.yaml":  "kind: Service\n",
	"demo/templates/dep.yaml":  "kind: Deployment\n",
	"demo/templates/sa.yaml":   "kind: ServiceAccount\n",
	"demo/templates/crds.yaml": "kind: CustomResourceDefinition\n",
}

func TestPullOCI(t *testing.T) {
	s := httptest.NewServer(registry.New())
	t.Cleanup(s.Close)
	u, _ := url.Parse(s.URL)
	host := u.Host

	tgz := makeTgz(t, demoChartFiles)
	layer := static.NewLayer(tgz, types.MediaType(ChartContentLayerMediaType))
	img := mutate.MediaType(empty.Image, types.OCIManifestSchema1)
	img = mutate.ConfigMediaType(img, types.MediaType(ChartConfigMediaType))
	img, err := mutate.AppendLayers(img, layer)
	if err != nil {
		t.Fatal(err)
	}
	if err := crane.Push(img, host+"/charts/demo:1.0.0"); err != nil {
		t.Fatal(err)
	}

	p := &Puller{Registry: ocireg.New(nil)}
	data, sha, err := p.Pull(context.Background(), "oci://"+host+"/charts", "demo", "1.0.0")
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if !bytes.Equal(data, tgz) {
		t.Error("pulled bytes differ from pushed tgz")
	}
	want := sha256.Sum256(tgz)
	if sha != hex.EncodeToString(want[:]) {
		t.Errorf("sha = %s", sha)
	}
}

func TestPullHTTPS(t *testing.T) {
	tgz := makeTgz(t, demoChartFiles)
	mux := http.NewServeMux()
	var repoURL string
	mux.HandleFunc("/index.yaml", func(w http.ResponseWriter, _ *http.Request) {
		// One absolute URL entry, one relative, one non-matching version.
		_, _ = fmt.Fprintf(w, `apiVersion: v1
entries:
  demo:
    - version: 0.9.0
      urls: ["demo-0.9.0.tgz"]
    - version: 1.0.0
      urls: ["%s/assets/demo-1.0.0.tgz"]
  relative:
    - version: 2.0.0
      urls: ["assets/relative-2.0.0.tgz"]
`, repoURL)
	})
	mux.HandleFunc("/assets/demo-1.0.0.tgz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(tgz)
	})
	mux.HandleFunc("/assets/relative-2.0.0.tgz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(tgz)
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	repoURL = s.URL

	p := &Puller{HTTP: s.Client()}
	data, _, err := p.Pull(context.Background(), s.URL, "demo", "1.0.0")
	if err != nil {
		t.Fatalf("Pull absolute: %v", err)
	}
	if !bytes.Equal(data, tgz) {
		t.Error("absolute-url pull differs")
	}

	data, _, err = p.Pull(context.Background(), s.URL, "relative", "2.0.0")
	if err != nil {
		t.Fatalf("Pull relative: %v", err)
	}
	if !bytes.Equal(data, tgz) {
		t.Error("relative-url pull differs")
	}

	if _, _, err := p.Pull(context.Background(), s.URL, "demo", "9.9.9"); err == nil {
		t.Error("want error for absent version")
	}
	if _, _, err := p.Pull(context.Background(), s.URL, "nochart", "1.0.0"); err == nil {
		t.Error("want error for absent chart")
	}

	versions, err := p.Versions(context.Background(), s.URL, "demo")
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if strings.Join(versions, ",") != "0.9.0,1.0.0" {
		t.Errorf("Versions = %v", versions)
	}
}

func TestExtractAndTreeDiff(t *testing.T) {
	tgz := makeTgz(t, demoChartFiles)
	a := t.TempDir()
	if err := Extract(tgz, a); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	for name, content := range demoChartFiles {
		got, err := os.ReadFile(filepath.Join(a, filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if string(got) != content {
			t.Errorf("%s content differs", name)
		}
	}

	b := t.TempDir()
	if err := Extract(tgz, b); err != nil {
		t.Fatal(err)
	}
	diffs, err := TreeDiff(a, b)
	if err != nil {
		t.Fatalf("TreeDiff: %v", err)
	}
	if len(diffs) != 0 {
		t.Errorf("identical trees diff: %v", diffs)
	}

	// Mutate one file, remove another, add a third.
	if err := os.WriteFile(filepath.Join(b, "demo/values.yaml"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(b, "demo/templates/cm.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b, "demo/extra.yaml"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	diffs, err = TreeDiff(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if len(diffs) != 3 {
		t.Errorf("diffs = %v", diffs)
	}

	if av, err := AppVersion(a, "demo"); err != nil || av != "v2.5.0" {
		t.Errorf("AppVersion = %q, %v", av, err)
	}
}

func TestExtractRejectsTraversal(t *testing.T) {
	for _, name := range []string{"../escape", "/abs", "a/../../up"} {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte("x"))
		_ = tw.Close()
		_ = gz.Close()
		if err := Extract(buf.Bytes(), t.TempDir()); err == nil {
			t.Errorf("Extract(%q): want traversal rejection", name)
		}
	}
}

func TestExtractRejectsSymlink(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	header := &tar.Header{Name: "demo/link", Linkname: "/etc/passwd", Typeflag: tar.TypeSymlink}
	if err := tw.WriteHeader(header); err != nil {
		t.Fatal(err)
	}
	_ = tw.Close()
	_ = gz.Close()
	if err := Extract(buf.Bytes(), t.TempDir()); err == nil {
		t.Error("want symlink rejection")
	}
}
