// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package build

import (
	"context"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/crane"

	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
)

// publish builds and publishes the platform manifests to registry a.
func (w *world) publish(t *testing.T) *Result {
	t.Helper()
	res, err := w.engine().Build(context.Background(), "platform", Options{Registries: []string{"a"}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return res
}

func TestPromote(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	built := w.publish(t)
	opts := PromoteOptions{From: "${VERSION}-edge.*", Tag: "v${VERSION}", Registries: []string{"a"}}

	// A dry run resolves and verifies, but tags nothing.
	opts.DryRun = true
	res, err := w.engine().Promote(ctx, "platform", opts)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if len(res.Records) != 1 || res.Records[0].Action != ActionWouldPromote {
		t.Fatalf("dry run records = %+v", res.Records)
	}
	if _, err := crane.Digest(w.host + "/a/manifests/platform:v1.4.0"); err == nil {
		t.Fatal("a dry run created the tag")
	}

	opts.DryRun = false
	res, err = w.engine().Promote(ctx, "platform", opts)
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	rec := res.Records[0]
	if rec.Action != ActionPromoted || rec.Digest != built.Digest ||
		rec.Source != w.host+"/a/manifests/platform:"+edgeTag ||
		rec.Ref != w.host+"/a/manifests/platform:v1.4.0" ||
		rec.Revision != "main@sha1:"+testSHA {
		t.Errorf("record = %+v (built %s)", rec, built.Digest)
	}
	if d, err := crane.Digest(rec.Ref); err != nil || d != built.Digest {
		t.Errorf("v1.4.0 = %s, %v; want %s", d, err, built.Digest)
	}
	// Promotion signs nothing new: the build's own signature is the one
	// that was verified.
	if len(w.signer.signed) != 1 {
		t.Errorf("signed = %v", w.signer.signed)
	}

	// A re-run is a no-op.
	res, err = w.engine().Promote(ctx, "platform", opts)
	if err != nil || res.Records[0].Action != ActionSkippedCurrent {
		t.Errorf("re-run = %+v, %v", res.Records, err)
	}
}

func TestPromoteNewestBuildAndTagsNeverMove(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.publish(t)
	if _, err := w.engine().Promote(ctx, "platform",
		PromoteOptions{From: "1.4.0-edge.*", Tag: "v1.4.0", Registries: []string{"a"}}); err != nil {
		t.Fatalf("Promote: %v", err)
	}

	// A later build of another commit: the newest edge build is now its.
	w.env["GITHUB_SHA"] = strings.Repeat("f", 40)
	w.env["PATCHY_TIMESTAMP"] = "20261111000000"
	later := w.publish(t)
	res, err := w.engine().Promote(ctx, "platform",
		PromoteOptions{From: "1.4.0-edge.*", Tag: "v1.4.1", Registries: []string{"a"}})
	if err != nil {
		t.Fatalf("Promote newest: %v", err)
	}
	if rec := res.Records[0]; rec.Digest != later.Digest ||
		!strings.HasSuffix(rec.Source, ":1.4.0-edge.20261111000000.gfffffff") {
		t.Errorf("newest = %+v, want %s", rec, later.Digest)
	}

	// v1.4.0 already names the first build: tags never move.
	if _, err := w.engine().Promote(ctx, "platform",
		PromoteOptions{From: "1.4.0-edge.*", Tag: "v1.4.0", Registries: []string{"a"}}); err == nil ||
		!strings.Contains(err.Error(), "tags never move") {
		t.Errorf("moving a tag = %v", err)
	}
}

func TestPromoteRefusals(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.publish(t)
	cases := []struct {
		name string
		opts PromoteOptions
		prep func()
		want string
	}{
		{"no match", PromoteOptions{From: "9.9.9-edge.*", Tag: "v9.9.9"}, nil, "no tag matches"},
		{"bad pattern", PromoteOptions{From: "[", Tag: "v1"}, nil, "not a valid tag pattern"},
		{"bad tag", PromoteOptions{From: "*", Tag: "v1/x"}, nil, "not a valid OCI tag"},
		{"unset variable", PromoteOptions{From: "${MISSING}-edge.*", Tag: "v1"}, nil, "--from"},
		{"unsigned build", PromoteOptions{From: "1.4.0-edge.*", Tag: "v1.4.0"},
			func() { w.signer.signed = nil }, "no valid signature"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.prep != nil {
				tc.prep()
			}
			tc.opts.Registries = []string{"a"}
			if _, err := w.engine().Promote(ctx, "platform", tc.opts); err == nil ||
				!strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
	if _, err := crane.Digest(w.host + "/a/manifests/platform:v1.4.0"); err == nil {
		t.Error("a refused promotion created the tag")
	}
}

func TestNewestMatch(t *testing.T) {
	cases := []struct {
		name    string
		tags    []string
		pattern string
		want    string
	}{
		{"semver timestamp order",
			[]string{"1.2.0-edge.20261109000000.gaaaaaaa", "1.2.0-edge.20261110000000.gbbbbbbb", "v1.1.0", "sha-x"},
			"1.2.0-edge.*", "1.2.0-edge.20261110000000.gbbbbbbb"},
		{"numeric, not lexical", []string{"1.2.0-edge.9", "1.2.0-edge.10"}, "1.2.0-edge.*", "1.2.0-edge.10"},
		{"lexical fallback", []string{"build-a", "build-c", "build-b"}, "build-*", "build-c"},
		{"never the target", []string{"1.0.0-rc.1", "1.0.0-rc.2"}, "1.0.0-rc.*", "1.0.0-rc.2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := newestMatch(tc.tags, tc.pattern, "1.0.0-rc.3")
			if err != nil || got != tc.want {
				t.Errorf("newestMatch = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
	if _, err := newestMatch([]string{"v1"}, "v1", "v1"); err == nil {
		t.Error("the target tag alone matched")
	}
}

func TestBuildDryRunWithoutRegistries(t *testing.T) {
	w := newWorld(t)
	w.global = &spec.Config{}
	if err := w.global.ResolveWithoutRegistries(); err != nil {
		t.Fatal(err)
	}
	res, err := w.engine().Build(context.Background(), "platform", Options{DryRun: true})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if res.Digest == "" || len(res.Records) != 0 {
		t.Errorf("result = %+v", res)
	}
	if err := (&spec.Config{}).Resolve(); err == nil {
		t.Error("Resolve accepted no registries")
	}
}
