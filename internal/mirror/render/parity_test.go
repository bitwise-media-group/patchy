// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package render

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"go.yaml.in/yaml/v3"
)

// TestMirrorStoreParity renders every vendored chart of a real mirror store
// and byte-compares the result with the committed rendered/manifests.yaml.
// It runs only when PATCHY_MIRROR_PARITY_DIR points at a checkout — the
// migration proof that this engine reproduces the trees the previous
// pipeline committed.
func TestMirrorStoreParity(t *testing.T) {
	root := os.Getenv("PATCHY_MIRROR_PARITY_DIR")
	if root == "" {
		t.Skip("PATCHY_MIRROR_PARITY_DIR not set")
	}
	dirs, err := filepath.Glob(filepath.Join(root, "charts", "*", "manifest.yaml"))
	if err != nil || len(dirs) == 0 {
		t.Fatalf("no chart manifests under %s (%v)", root, err)
	}
	for _, manifestPath := range dirs {
		entryDir := filepath.Dir(manifestPath)
		t.Run(filepath.Base(entryDir), func(t *testing.T) {
			raw, err := os.ReadFile(manifestPath)
			if err != nil {
				t.Fatal(err)
			}
			// Decode leniently: the store may still carry its previous
			// schema during migration; discovery fields are unchanged.
			var m struct {
				Chart struct {
					Name string `yaml:"name"`
				} `yaml:"chart"`
				Discovery struct {
					Namespace   string   `yaml:"namespace"`
					ValuesFiles []string `yaml:"valuesFiles"`
					KubeVersion string   `yaml:"kubeVersion"`
					APIVersions []string `yaml:"apiVersions"`
				} `yaml:"discovery"`
			}
			if err := yaml.Unmarshal(raw, &m); err != nil {
				t.Fatal(err)
			}
			namespace := m.Discovery.Namespace
			if namespace == "" {
				namespace = filepath.Base(entryDir)
			}
			kubeVersion := m.Discovery.KubeVersion
			if kubeVersion == "" {
				kubeVersion = "1.34.0"
			}
			valuesFiles := make([]string, 0, len(m.Discovery.ValuesFiles))
			for _, f := range m.Discovery.ValuesFiles {
				valuesFiles = append(valuesFiles, filepath.Join(entryDir, f))
			}
			got, err := Render(context.Background(), Input{
				ChartDir:    filepath.Join(entryDir, "vendor", m.Chart.Name),
				ReleaseName: m.Chart.Name,
				Namespace:   namespace,
				KubeVersion: kubeVersion,
				APIVersions: m.Discovery.APIVersions,
				ValuesFiles: valuesFiles,
			})
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			want, err := os.ReadFile(filepath.Join(entryDir, "rendered", "manifests.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(want) {
				t.Errorf("rendered output differs from committed manifests.yaml (%d vs %d bytes)", len(got), len(want))
				diffDir := t.TempDir()
				_ = os.WriteFile(filepath.Join(diffDir, "got.yaml"), got, 0o644)
				t.Logf("wrote regenerated output to %s/got.yaml for diffing", diffDir)
			}
		})
	}
}
