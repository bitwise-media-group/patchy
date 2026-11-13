// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Extensions lists the accepted config formats, in the order a location is
// probed. More than one present at one location is ambiguous.
var Extensions = []string{"yaml", "yml", "json", "toml"}

// ErrAmbiguous reports one location holding the config in more than one
// format; the CLI treats it as a usage error.
var ErrAmbiguous = errors.New("ambiguous config")

// Options locates the config roots. Every field is injectable so tests can
// point the search at temp directories; zero values resolve to the real
// process working directory, the user's home, and /etc/patchy.
type Options struct {
	// Workdir is the project root the four workdir locations hang off
	// and relative config paths resolve against (default: os.Getwd).
	Workdir string
	// Home is the user's home (default: os.UserHomeDir; none skips the
	// user layer).
	Home string
	// SystemDir is the system-wide config directory (default /etc/patchy).
	SystemDir string
}

// defaultSystemDir is the system-wide config directory.
const defaultSystemDir = "/etc/patchy"

// withDefaults resolves the zero-valued roots.
func (o Options) withDefaults() (Options, error) {
	if o.Workdir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return o, fmt.Errorf("resolve working directory: %w", err)
		}
		o.Workdir = wd
	}
	abs, err := filepath.Abs(o.Workdir)
	if err != nil {
		return o, err
	}
	o.Workdir = abs
	if o.Home == "" {
		if home, err := os.UserHomeDir(); err == nil {
			o.Home = home
		}
	}
	if o.SystemDir == "" {
		o.SystemDir = defaultSystemDir
	}
	return o, nil
}

// location is one search position: the file <dir>/<base>.<ext>.
type location struct {
	dir, base string
}

// locations lists the search positions, least to most preferred.
func (o Options) locations() []location {
	locs := []location{{dir: o.SystemDir, base: "config"}}
	if o.Home != "" {
		locs = append(locs, location{dir: filepath.Join(o.Home, ".config", "patchy"), base: "config"})
	}
	return append(locs,
		location{dir: filepath.Join(o.Workdir, "mise"), base: "patchy"},
		location{dir: filepath.Join(o.Workdir, "patchy"), base: "config"},
		location{dir: filepath.Join(o.Workdir, ".mise"), base: "patchy"},
		location{dir: filepath.Join(o.Workdir, ".patchy"), base: "config"},
		location{dir: o.Workdir, base: "patchy"},
	)
}

// Loaded is a resolved configuration.
type Loaded struct {
	// File is the merged config, strictly decoded.
	File *File
	// Merged is the merged generic map — what viper layers flags and
	// environment over for the flag-backed keys.
	Merged map[string]any
	// Sources lists the loaded layers in precedence order (least
	// preferred first), as display paths.
	Sources []string
	// Warnings are non-fatal findings for narration (a legacy
	// .patchy.yaml that is no longer read).
	Warnings []string
	// Workdir is the absolute working directory relative paths resolve
	// against.
	Workdir string
	// projectLayers counts the loaded layers inside the working directory.
	projectLayers int
}

// HasProjectLayer reports whether any loaded layer lives in the working
// directory (as opposed to /etc or $HOME) — mirror add scaffolds a project
// patchy.yaml only when none does.
func (l *Loaded) HasProjectLayer() bool { return l.projectLayers > 0 }

// Path resolves a config path (mirror.directory, a dockerfile, a manifests
// path) against the working directory; every layer's relative paths mean
// the same thing.
func (l *Loaded) Path(p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(l.Workdir, p)
}

// layer is one config file found on the search path.
type layer struct {
	path, ext string
}

// Load finds, decodes, checks and merges every config layer. No file at
// all is an empty config, not an error.
func Load(o Options) (*Loaded, error) {
	o, err := o.withDefaults()
	if err != nil {
		return nil, err
	}
	layers, err := find(o)
	if err != nil {
		return nil, err
	}
	loaded := &Loaded{Merged: map[string]any{}, Workdir: o.Workdir}
	var enforced []enforcedPolicy
	for _, l := range layers {
		shown := o.display(l.path)
		raw, err := os.ReadFile(l.path)
		if err != nil {
			return nil, fmt.Errorf("read config %s: %w", shown, err)
		}
		m, err := decodeLayer(l.ext, raw)
		if err != nil {
			return nil, fmt.Errorf("parse config %s: %w", shown, err)
		}
		// Strict per-layer decode: an unknown key is reported against the
		// file that carries it, not the merged result.
		layerFile, err := decodeFile(Merge(nil, m))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", shown, err)
		}
		if e, ok := enforcedPolicies(shown, layerFile); ok {
			enforced = append(enforced, e)
		}
		loaded.Merged = Merge(loaded.Merged, m)
		loaded.Sources = append(loaded.Sources, shown)
		if _, ok := under(o.Workdir, l.path); ok {
			loaded.projectLayers++
		}
	}
	file, err := decodeFile(loaded.Merged)
	if err != nil {
		return nil, fmt.Errorf("patchy config (merged from %s): %w", strings.Join(loaded.Sources, ", "), err)
	}
	file.enforced = enforced
	loaded.File = file
	loaded.Warnings = legacyWarnings(o)
	return loaded, nil
}

// find probes every location, rejecting ambiguity and loading a file that
// two locations resolve to only once — at its more preferred position.
func find(o Options) ([]layer, error) {
	var layers []layer
	index := map[string]int{}
	for _, loc := range o.locations() {
		var found []layer
		for _, ext := range Extensions {
			path := filepath.Join(loc.dir, loc.base+"."+ext)
			info, err := os.Stat(path)
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) {
					continue
				}
				return nil, fmt.Errorf("probe config %s: %w", o.display(path), err)
			}
			if !info.IsDir() {
				found = append(found, layer{path: path, ext: ext})
			}
		}
		switch len(found) {
		case 0:
			continue
		case 1:
		default:
			shown := make([]string, len(found))
			for i, l := range found {
				shown[i] = o.display(l.path)
			}
			return nil, fmt.Errorf("%w: found %s; keep exactly one", ErrAmbiguous, strings.Join(shown, ", "))
		}
		key := canonical(found[0].path)
		if i, ok := index[key]; ok {
			layers = append(layers[:i], layers[i+1:]...)
			for k, j := range index {
				if j > i {
					index[k] = j - 1
				}
			}
		}
		index[key] = len(layers)
		layers = append(layers, found[0])
	}
	return layers, nil
}

// canonical resolves symlinks so two locations naming one file dedupe.
func canonical(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return filepath.Clean(path)
}

// display shortens a path for messages: relative under the working
// directory, ~-prefixed under the home directory, absolute otherwise.
func (o Options) display(path string) string {
	if rel, ok := under(o.Workdir, path); ok {
		return rel
	}
	if o.Home != "" {
		if rel, ok := under(o.Home, path); ok {
			return filepath.Join("~", rel)
		}
	}
	return path
}

// under reports path relative to root when it lies beneath it.
func under(root, path string) (string, bool) {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", false
	}
	return rel, true
}
