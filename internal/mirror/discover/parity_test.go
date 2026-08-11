// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package discover

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/bitwise-media-group/patchy/internal/mirror/helmchart"
	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
)

// TestMirrorStoreParity discovers images from a real mirror store's
// committed rendered trees and compares the result with the committed
// images.lock.yaml sources. Runs only when PATCHY_MIRROR_PARITY_DIR points
// at a checkout.
func TestMirrorStoreParity(t *testing.T) {
	root := os.Getenv("PATCHY_MIRROR_PARITY_DIR")
	if root == "" {
		t.Skip("PATCHY_MIRROR_PARITY_DIR not set")
	}
	manifests, err := filepath.Glob(filepath.Join(root, "charts", "*", "manifest.yaml"))
	if err != nil || len(manifests) == 0 {
		t.Fatalf("no chart manifests under %s (%v)", root, err)
	}
	for _, manifestPath := range manifests {
		entryDir := filepath.Dir(manifestPath)
		t.Run(filepath.Base(entryDir), func(t *testing.T) {
			raw, err := os.ReadFile(manifestPath)
			if err != nil {
				t.Fatal(err)
			}
			// Lenient decode: the store may carry its previous schema.
			var m struct {
				Chart struct {
					Name string `yaml:"name"`
				} `yaml:"chart"`
				Images struct {
					Extra      []spec.ExtraImage     `yaml:"extra"`
					Exclude    []spec.ExcludePattern `yaml:"exclude"`
					AllowEmpty bool                  `yaml:"allowEmpty"`
				} `yaml:"images"`
			}
			if err := yaml.Unmarshal(raw, &m); err != nil {
				t.Fatal(err)
			}
			rendered, err := os.ReadFile(filepath.Join(entryDir, "rendered", "manifests.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			appVersion, err := helmchart.AppVersion(filepath.Join(entryDir, "vendor"), m.Chart.Name)
			if err != nil {
				t.Fatal(err)
			}
			// Machine-derived extras live in the generated sidecar and
			// merge with the manifest's own, exactly as the engine does.
			sidecar, err := spec.LoadSidecar(entryDir)
			if err != nil {
				t.Fatal(err)
			}
			got, err := Discover(Input{
				Rendered:   rendered,
				AppVersion: appVersion,
				Extra:      append(append([]spec.ExtraImage{}, m.Images.Extra...), sidecar.Extra...),
				Exclude:    m.Images.Exclude,
				AllowEmpty: m.Images.AllowEmpty,
			})
			if err != nil {
				t.Fatalf("Discover: %v", err)
			}
			lock, err := spec.LoadImagesLock(filepath.Join(entryDir, "images.lock.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			var want []string
			for _, img := range lock.Images {
				want = append(want, img.Source)
			}
			if strings.Join(got.Images, "\n") != strings.Join(want, "\n") {
				t.Errorf("discovered images differ from lock:\n--- got ---\n%s\n--- want ---\n%s",
					strings.Join(got.Images, "\n"), strings.Join(want, "\n"))
			}
		})
	}
}
