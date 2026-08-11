// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package ocireg

import (
	"context"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// newRegistry starts an in-memory registry and returns its host.
func newRegistry(t *testing.T) string {
	t.Helper()
	s := httptest.NewServer(registry.New(registry.WithReferrersSupport(true)))
	t.Cleanup(s.Close)
	u, err := url.Parse(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host
}

// pushRandom pushes a random single-platform image and returns its digest.
func pushRandom(t *testing.T, ref string) string {
	t.Helper()
	img, err := random.Image(256, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := crane.Push(img, ref); err != nil {
		t.Fatalf("push %s: %v", ref, err)
	}
	d, err := img.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return d.String()
}

func TestDigestAndExists(t *testing.T) {
	host := newRegistry(t)
	ctx := context.Background()
	c := New(nil)
	ref := host + "/app:v1"
	want := pushRandom(t, ref)

	got, err := c.Digest(ctx, ref)
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if got != want {
		t.Errorf("Digest = %s, want %s", got, want)
	}

	d, ok, err := c.Exists(ctx, ref)
	if err != nil || !ok || d != want {
		t.Errorf("Exists = (%s, %v, %v)", d, ok, err)
	}
	_, ok, err = c.Exists(ctx, host+"/app:missing")
	if err != nil || ok {
		t.Errorf("Exists(missing) = (%v, %v), want (false, nil)", ok, err)
	}
	_, ok, err = c.Exists(ctx, host+"/norepo:v1")
	if err != nil || ok {
		t.Errorf("Exists(norepo) = (%v, %v), want (false, nil)", ok, err)
	}
}

func TestTags(t *testing.T) {
	host := newRegistry(t)
	c := New(nil)
	for _, tag := range []string{"1.0.0", "1.1.0", "latest"} {
		pushRandom(t, host+"/app:"+tag)
	}
	tags, err := c.Tags(context.Background(), host+"/app")
	if err != nil {
		t.Fatalf("Tags: %v", err)
	}
	sort.Strings(tags)
	if strings.Join(tags, ",") != "1.0.0,1.1.0,latest" {
		t.Errorf("Tags = %v", tags)
	}
}

func TestPlatforms(t *testing.T) {
	host := newRegistry(t)
	ctx := context.Background()
	c := New(nil)

	// A multi-platform index, plus an "unknown" attestation entry that
	// must be excluded.
	idx := mutate.IndexMediaType(empty.Index, types.OCIImageIndex)
	for _, p := range []v1.Platform{
		{OS: "linux", Architecture: "amd64"},
		{OS: "linux", Architecture: "arm64"},
		{OS: "unknown", Architecture: "unknown"},
	} {
		img, err := random.Image(64, 1)
		if err != nil {
			t.Fatal(err)
		}
		idx = mutate.AppendManifests(idx, mutate.IndexAddendum{
			Add:        img,
			Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: p.OS, Architecture: p.Architecture}},
		})
	}
	ref := host + "/multi:v1"
	if err := pushIndex(idx, ref); err != nil {
		t.Fatalf("push index: %v", err)
	}

	got, err := c.Platforms(ctx, ref)
	if err != nil {
		t.Fatalf("Platforms: %v", err)
	}
	if strings.Join(got, ",") != "linux/amd64,linux/arm64" {
		t.Errorf("Platforms = %v", got)
	}

	// Single-platform image: empty list.
	single := host + "/single:v1"
	pushRandom(t, single)
	got, err = c.Platforms(ctx, single)
	if err != nil {
		t.Fatalf("Platforms(single): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Platforms(single) = %v, want empty", got)
	}
}

func pushIndex(idx v1.ImageIndex, ref string) error {
	r, err := name.ParseReference(ref)
	if err != nil {
		return err
	}
	return remote.WriteIndex(r, idx)
}

func TestCreated(t *testing.T) {
	host := newRegistry(t)
	ctx := context.Background()
	c := New(nil)

	created := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	img, err := random.Image(64, 1)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := img.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	cfg = cfg.DeepCopy()
	cfg.Created = v1.Time{Time: created}
	img, err = mutate.ConfigFile(img, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ref := host + "/dated:v1"
	if err := crane.Push(img, ref); err != nil {
		t.Fatal(err)
	}

	ts, ok, err := c.Created(ctx, ref)
	if err != nil || !ok {
		t.Fatalf("Created = (%v, %v, %v)", ts, ok, err)
	}
	if !ts.Equal(created) {
		t.Errorf("Created = %v, want %v", ts, created)
	}
}

func TestCopyPreservesDigest(t *testing.T) {
	host := newRegistry(t)
	ctx := context.Background()
	c := New(nil)
	src := host + "/src/app:v1"
	want := pushRandom(t, src)

	dst := host + "/mirror/src/app:v1"
	if err := c.Copy(ctx, src, dst); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	got, err := c.Digest(ctx, dst)
	if err != nil {
		t.Fatalf("Digest(dst): %v", err)
	}
	if got != want {
		t.Errorf("copied digest = %s, want %s", got, want)
	}
}

func TestConfigMediaTypeAndLayerBytes(t *testing.T) {
	host := newRegistry(t)
	ctx := context.Background()
	c := New(nil)

	// Build a helm-chart-shaped artifact: config media type + one tgz layer.
	const chartConfig = "application/vnd.cncf.helm.config.v1+json"
	const chartLayer = "application/vnd.cncf.helm.chart.content.v1.tar+gzip"
	payload := []byte("not-really-a-tgz-but-bytes")
	layer := static.NewLayer(payload, types.MediaType(chartLayer))
	img := mutate.MediaType(empty.Image, types.OCIManifestSchema1)
	img = mutate.ConfigMediaType(img, types.MediaType(chartConfig))
	img, err := mutate.AppendLayers(img, layer)
	if err != nil {
		t.Fatal(err)
	}
	ref := host + "/charts/demo:1.0.0"
	if err := crane.Push(img, ref); err != nil {
		t.Fatal(err)
	}

	mt, err := c.ConfigMediaType(ctx, ref)
	if err != nil {
		t.Fatalf("ConfigMediaType: %v", err)
	}
	if mt != chartConfig {
		t.Errorf("ConfigMediaType = %q", mt)
	}

	data, err := c.LayerBytes(ctx, ref, chartLayer)
	if err != nil {
		t.Fatalf("LayerBytes: %v", err)
	}
	if string(data) != string(payload) {
		t.Errorf("LayerBytes = %q", data)
	}

	if _, err := c.LayerBytes(ctx, ref, "application/other"); err == nil {
		t.Error("LayerBytes with absent media type: want error")
	}
}
