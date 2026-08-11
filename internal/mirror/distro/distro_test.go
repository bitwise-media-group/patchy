// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package distro

import (
	"archive/tar"
	"bytes"
	"strings"
	"testing"
)

// makeArchive builds a flattened-filesystem tar of path->content.
func makeArchive(t *testing.T, files map[string]string) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, content := range files {
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
	return &buf
}

const sourceController = `apiVersion: v1
kind: ServiceAccount
metadata:
  name: source-controller
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: source-controller
spec:
  template:
    spec:
      containers:
        - name: manager
          image: fluxcd/source-controller:v1.9.3
`

func TestDerive(t *testing.T) {
	archive := makeArchive(t, map[string]string{
		// Older version tree must be ignored; newest wins.
		"flux/v2.8.0/source-controller.yaml": strings.ReplaceAll(sourceController, "v1.9.3", "v1.8.0"),
		"flux/v2.9.3/source-controller.yaml": sourceController,
		"flux/v2.9.3/helm-controller.yaml": strings.ReplaceAll(
			strings.ReplaceAll(sourceController, "source-controller", "helm-controller"), "v1.9.3", "v1.6.3"),
	})
	extras, err := Derive(archive, []string{"source-controller", "helm-controller"}, "",
		"oci://ghcr.io/example/manifests:v0.56.0")
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if len(extras) != 2 {
		t.Fatalf("extras = %d", len(extras))
	}
	if extras[0].Image != "ghcr.io/fluxcd/source-controller:v1.9.3" {
		t.Errorf("extras[0] = %+v", extras[0])
	}
	wantReason := "flux v2.9.3 distribution controller (derived from oci://ghcr.io/example/manifests:v0.56.0)"
	if extras[0].Reason != wantReason {
		t.Errorf("reason = %q\nwant %q", extras[0].Reason, wantReason)
	}
	if extras[1].Image != "ghcr.io/fluxcd/helm-controller:v1.6.3" {
		t.Errorf("extras[1] = %+v", extras[1])
	}
}

func TestDeriveRegistryQualifiedRefSurvives(t *testing.T) {
	archive := makeArchive(t, map[string]string{
		"prod/v1.0.0/ctrl.yaml": strings.ReplaceAll(sourceController,
			"fluxcd/source-controller:v1.9.3", "quay.io/org/ctrl:v2"),
	})
	extras, err := Derive(archive, []string{"ctrl"}, "", "oci://x/y:z")
	if err != nil {
		t.Fatal(err)
	}
	if extras[0].Image != "quay.io/org/ctrl:v2" {
		t.Errorf("qualified ref was rewritten: %q", extras[0].Image)
	}
}

func TestDeriveErrors(t *testing.T) {
	t.Run("missing component", func(t *testing.T) {
		archive := makeArchive(t, map[string]string{"flux/v2.9.3/a.yaml": sourceController})
		if _, err := Derive(archive, []string{"missing"}, "", "ref"); err == nil {
			t.Error("want error")
		}
	})
	t.Run("two product trees", func(t *testing.T) {
		archive := makeArchive(t, map[string]string{
			"a/v1.0.0/x.yaml": sourceController,
			"b/v1.0.0/x.yaml": sourceController,
		})
		if _, err := Derive(archive, []string{"x"}, "", "ref"); err == nil {
			t.Error("want error")
		}
	})
	t.Run("no deployment image", func(t *testing.T) {
		archive := makeArchive(t, map[string]string{"p/v1.0.0/x.yaml": "kind: ServiceAccount\n"})
		if _, err := Derive(archive, []string{"x"}, "", "ref"); err == nil {
			t.Error("want error")
		}
	})
}
