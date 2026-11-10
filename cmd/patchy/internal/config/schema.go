// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package config

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/go-viper/mapstructure/v2"

	"github.com/bitwise-media-group/patchy/internal/build"
	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
)

// File is the patchy config's shape: the blocks mirror and build share at
// the top level, then one block per command group.
type File struct {
	// Registries are where mirror and build publish; every entry goes to
	// every registry (or the --registry selection).
	Registries []spec.Registry `yaml:"registries"`
	// Signing is the default signing block; a registry's own replaces it.
	Signing spec.Signing `yaml:"signing"`
	// Scan holds the scanner roster and the defaults every entry's scan
	// block overrides field by field.
	Scan   spec.Scan  `yaml:"scan"`
	Mirror Mirror     `yaml:"mirror"`
	Build  build.Spec `yaml:"build"`
	Dev    Dev        `yaml:"dev"`
}

// Mirror is the mirror: block.
type Mirror struct {
	// Directory is the mirror store root, relative to the working
	// directory (default: the working directory). Also --directory/-C.
	Directory string `yaml:"directory"`
	// Workers bounds concurrent registry operations (also --workers).
	Workers int `yaml:"workers"`
	// Update tunes the tracked-tag soak window.
	Update spec.Update `yaml:"update"`
	// SourceRegistryRewrites reroutes upstream pulls (e.g. through a
	// pull-through cache); recorded sources keep the canonical path.
	SourceRegistryRewrites map[string]string `yaml:"sourceRegistryRewrites"`
}

// Dev is the dev: block: one key per dev-command flag. The CLI reads the
// values through viper (flag > env > file > default); the types here exist
// so an unknown or mistyped key fails against the file that carries it.
type Dev struct {
	Addr           string        `yaml:"addr"`
	Name           string        `yaml:"name"`
	Secret         string        `yaml:"secret"`
	SecretFile     string        `yaml:"secret-file"`
	MinSeverity    string        `yaml:"min-severity"`
	EnhanceURL     string        `yaml:"enhance-url"`
	EnhanceTimeout time.Duration `yaml:"enhance-timeout"`
	ResolveURL     string        `yaml:"resolve-url"`
	ResolveTimeout time.Duration `yaml:"resolve-timeout"`
	ResolveDelay   time.Duration `yaml:"resolve-delay"`
	NoAutoResolve  bool          `yaml:"no-auto-resolve"`
}

// MirrorConfig assembles and validates the mirror's view: the shared
// registries, signing and scan blocks plus the mirror: block's update and
// rewrites. A config without registries fails here, never at load.
func (f *File) MirrorConfig() (*spec.Config, error) {
	c := &spec.Config{
		Registries:             slices.Clone(f.Registries),
		Signing:                f.Signing,
		Update:                 f.Mirror.Update,
		Scan:                   f.Scan,
		SourceRegistryRewrites: f.Mirror.SourceRegistryRewrites,
	}
	if err := c.Resolve(); err != nil {
		return nil, fmt.Errorf("patchy config: %w", err)
	}
	return c, nil
}

// BuildConfig returns the shared registries, signing and scan blocks
// (resolved) plus the validated build spec.
func (f *File) BuildConfig() (*spec.Config, *build.Spec, error) {
	return f.buildConfig((*spec.Config).Resolve)
}

// BuildPlanConfig is BuildConfig for a run that never contacts a registry
// (`patchy build --dry-run`): registries may be absent, so CI and a
// workstation can build and scan with no registry configured at all.
func (f *File) BuildPlanConfig() (*spec.Config, *build.Spec, error) {
	return f.buildConfig((*spec.Config).ResolveWithoutRegistries)
}

// buildConfig assembles the build's view, resolving the shared blocks with
// the given resolver.
func (f *File) buildConfig(resolve func(*spec.Config) error) (*spec.Config, *build.Spec, error) {
	c := &spec.Config{
		Registries: slices.Clone(f.Registries),
		Signing:    f.Signing,
		Scan:       f.Scan,
	}
	if err := resolve(c); err != nil {
		return nil, nil, fmt.Errorf("patchy config: %w", err)
	}
	s := f.Build
	if err := s.Validate(c.Scan); err != nil {
		return nil, nil, fmt.Errorf("patchy config: %w", err)
	}
	return c, &s, nil
}

// decodeFile strictly decodes a generic map into File: unknown keys are
// errors, durations parse from strings, and a scan.enabled switch accepts a
// bool as well as auto|true|false.
func decodeFile(m map[string]any) (*File, error) {
	var f File
	dec, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		TagName:         "yaml",
		SquashTagOption: "inline",
		ErrorUnused:     true,
		Result:          &f,
		DecodeHook: mapstructure.ComposeDecodeHookFunc(
			scanEnabledHook,
			mapstructure.StringToTimeDurationHookFunc(),
		),
	})
	if err != nil {
		return nil, err
	}
	if err := dec.Decode(m); err != nil {
		return nil, decodeError(err, m)
	}
	return &f, nil
}

// scanEnabledType is the tri-state switch's reflected type.
var scanEnabledType = reflect.TypeFor[spec.ScanEnabled]()

// scanEnabledHook decodes a bool or string into spec.ScanEnabled.
func scanEnabledHook(_ reflect.Type, to reflect.Type, data any) (any, error) {
	if to != scanEnabledType {
		return data, nil
	}
	return spec.ParseScanEnabled(data)
}

// decodeError flattens mapstructure's aggregate error into one line per
// problem and appends a pointed hint for keys the config moved.
func decodeError(err error, m map[string]any) error {
	if inner := errors.Unwrap(err); inner != nil {
		err = inner
	}
	var lines []string
	for line := range strings.SplitSeq(err.Error(), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, strings.Replace(line, "'config.File' has invalid keys", "unknown top-level keys", 1))
		}
	}
	msg := strings.Join(lines, "; ")
	if hints := movedKeyHints(m); len(hints) > 0 {
		msg += " (" + strings.Join(hints, "; ") + ")"
	}
	return errors.New(msg)
}

// movedKeys maps removed key paths to where they live now.
var movedKeys = []struct{ path, now string }{
	{"scan.allowlistMaxDays", "scan.allowlist.maxDays"},
	{"scan.allowlistNewDays", "scan.allowlist.newDays"},
	{"update", "mirror.update"},
	{"sourceRegistryRewrites", "mirror.sourceRegistryRewrites"},
	{"apiVersion", "nothing: the patchy config has no apiVersion/kind header; delete it"},
	{"kind", "nothing: the patchy config has no apiVersion/kind header; delete it"},
}

// movedKeyHints names every removed key present in m with its new home.
func movedKeyHints(m map[string]any) []string {
	var hints []string
	seen := map[string]bool{}
	for _, mk := range movedKeys {
		if !hasPath(m, mk.path) {
			continue
		}
		hint := mk.path + " moved to " + mk.now
		if strings.HasPrefix(mk.now, "nothing") {
			hint = mk.path + " is not a patchy config key (" + strings.TrimPrefix(mk.now, "nothing: ") + ")"
		}
		if !seen[hint] {
			seen[hint] = true
			hints = append(hints, hint)
		}
	}
	return hints
}

// hasPath reports whether a dotted key path exists in m.
func hasPath(m map[string]any, path string) bool {
	head, rest, nested := strings.Cut(path, ".")
	v, ok := m[head]
	if !ok {
		return false
	}
	if !nested {
		return true
	}
	sub, ok := v.(map[string]any)
	return ok && hasPath(sub, rest)
}
