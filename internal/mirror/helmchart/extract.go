// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package helmchart

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Extract unpacks a chart tgz into destDir the way tar -xzf does, minus
// the hazards: entry paths must stay inside destDir (no absolute paths, no
// .. traversal), and only regular files and directories are materialized —
// a chart archive has no business carrying symlinks or devices.
func Extract(tgz []byte, destDir string) error {
	gz, err := gzip.NewReader(bytes.NewReader(tgz))
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}
		name := filepath.FromSlash(hdr.Name)
		if !filepath.IsLocal(name) {
			return fmt.Errorf("archive entry %q escapes the destination", hdr.Name)
		}
		target := filepath.Join(destDir, name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := fs.FileMode(hdr.Mode) & 0o777
			if mode == 0 {
				mode = 0o644
			}
			f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				_ = f.Close()
				return fmt.Errorf("write %s: %w", target, err)
			}
			if err := f.Close(); err != nil {
				return err
			}
		case tar.TypeXGlobalHeader:
			// pax global headers carry no content.
		default:
			return fmt.Errorf("archive entry %q has unsupported type %c", hdr.Name, hdr.Typeflag)
		}
	}
}

// TreeDiff compares two directory trees by relative path and content,
// returning the differing paths ("only in a", "only in b", "differs").
// Empty result means identical trees.
func TreeDiff(aDir, bDir string) ([]string, error) {
	aFiles, err := treeFiles(aDir)
	if err != nil {
		return nil, err
	}
	bFiles, err := treeFiles(bDir)
	if err != nil {
		return nil, err
	}
	var diffs []string
	for _, p := range aFiles {
		if !contains(bFiles, p) {
			diffs = append(diffs, p+": only in "+aDir)
		}
	}
	for _, p := range bFiles {
		if !contains(aFiles, p) {
			diffs = append(diffs, p+": only in "+bDir)
			continue
		}
		ab, err := os.ReadFile(filepath.Join(aDir, p))
		if err != nil {
			return nil, err
		}
		bb, err := os.ReadFile(filepath.Join(bDir, p))
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(ab, bb) {
			diffs = append(diffs, p+": differs")
		}
	}
	sort.Strings(diffs)
	return diffs, nil
}

// treeFiles lists a tree's regular files as sorted relative slash paths.
func treeFiles(dir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			rel, err := filepath.Rel(dir, path)
			if err != nil {
				return err
			}
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", dir, err)
	}
	sort.Strings(files)
	return files, nil
}

// contains reports membership in a sorted list.
func contains(sorted []string, s string) bool {
	i := sort.SearchStrings(sorted, s)
	return i < len(sorted) && sorted[i] == s
}

// ChartYAML reads a field map from the extracted chart's Chart.yaml.
func ChartYAML(vendorDir, chartName string) (map[string]any, error) {
	raw, err := os.ReadFile(filepath.Join(vendorDir, chartName, "Chart.yaml"))
	if err != nil {
		return nil, fmt.Errorf("read Chart.yaml: %w", err)
	}
	var m map[string]any
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse Chart.yaml: %w", err)
	}
	return m, nil
}

// AppVersion reads the extracted chart's appVersion ("" when absent).
func AppVersion(vendorDir, chartName string) (string, error) {
	m, err := ChartYAML(vendorDir, chartName)
	if err != nil {
		return "", err
	}
	v, _ := m["appVersion"].(string)
	return strings.TrimSpace(v), nil
}
