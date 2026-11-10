// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package spec

import (
	"fmt"
	"slices"

	"go.yaml.in/yaml/v4"
)

// Scan is the config's scan block: the scanner roster plus the defaults
// every entry inherits. Entries override individual fields with the same
// shape (ScanOverride); EffectiveScanPolicy resolves the two.
type Scan struct {
	// Enabled is the master default (default true): false turns scanning
	// off for every entry that does not say otherwise. With scanning
	// enabled, at least one image scanner must be enabled in Scanners.
	Enabled  *bool    `yaml:"enabled"`
	Scanners Scanners `yaml:"scanners"`
	// FailOn lists blocking severities (default CRITICAL, HIGH).
	FailOn []string `yaml:"failOn"`
	// IgnoreUnfixed skips findings with no fixed version (default true).
	IgnoreUnfixed *bool `yaml:"ignoreUnfixed"`
	// Allowlist holds the accepted-risk defaults.
	Allowlist AllowlistPolicy `yaml:"allowlist"`
}

// Scanners toggles the pluggable scanners independently; none is forced,
// but an image scan with neither osv nor grype enabled is an error.
type Scanners struct {
	// OSV shells out to osv-scanner, which must be on PATH when enabled
	// (default disabled).
	OSV *ScannerToggle `yaml:"osv"`
	// Grype shells out to grype, which must be on PATH when enabled
	// (default disabled).
	Grype *ScannerToggle `yaml:"grype"`
	// Kubescape scans rendered manifests for misconfigurations (default
	// enabled, warn-only; skipped with a warning when the binary is absent).
	Kubescape *KubescapeScanner `yaml:"kubescape"`
}

// ScannerToggle enables or disables one scanner.
type ScannerToggle struct {
	Enabled bool `yaml:"enabled"`
}

// KubescapeScanner configures the configuration scan.
type KubescapeScanner struct {
	Enabled bool `yaml:"enabled"`
	// Mode is "warn" (findings inform review) or "fail" (findings block).
	Mode string `yaml:"mode"`
}

// OSVEnabled resolves the osv default (off).
func (s Scanners) OSVEnabled() bool { return s.OSV != nil && s.OSV.Enabled }

// GrypeEnabled resolves the grype default (off).
func (s Scanners) GrypeEnabled() bool { return s.Grype != nil && s.Grype.Enabled }

// KubescapeEnabled resolves the kubescape default (on).
func (s Scanners) KubescapeEnabled() bool { return s.Kubescape == nil || s.Kubescape.Enabled }

// KubescapeMode resolves the kubescape mode default ("warn").
func (s Scanners) KubescapeMode() string {
	if s.Kubescape == nil || s.Kubescape.Mode == "" {
		return "warn"
	}
	return s.Kubescape.Mode
}

// EffectiveEnabled resolves the master scanning default (on).
func (s Scan) EffectiveEnabled() bool {
	return s.Enabled == nil || *s.Enabled
}

// AllowlistPolicy is the accepted-risk block, in the config (defaults) and
// on every entry (overrides). Pointers keep "unset — inherit" distinct from
// an explicit false, 0 or "".
type AllowlistPolicy struct {
	// MaxDays bounds every allowlist entry's expired_at horizon (default
	// 90) — expiry forces re-review instead of accepted-forever CVEs.
	MaxDays *int `yaml:"maxDays"`
	// NewDays is the expiry stamped on new derived entries (default 90).
	// Surviving entries keep the date they were first accepted with, so
	// regeneration never rolls a live acceptance forward — except one
	// whose date has already lapsed (on or before today), which is renewed
	// to this horizon as a fresh acceptance, reviewed as a date change in
	// the diff.
	NewDays *int `yaml:"newDays"`
	// Generate regenerates the entry's allowlist from a fresh scan (mirror
	// upgrade, build --update-allowlists); the PR review is where the
	// risk is actually accepted (default false).
	Generate *bool `yaml:"generate"`
	// Preamble is whole-file context rendered as comments atop the
	// generated allowlist (default none).
	Preamble *string `yaml:"preamble"`
}

// IsSet reports whether any field is set.
func (p AllowlistPolicy) IsSet() bool {
	return p.MaxDays != nil || p.NewDays != nil || p.Generate != nil || p.Preamble != nil
}

// ScanEnabled is an entry's tri-state scan switch: auto, true or false. It
// decodes from a YAML bool as well as a string; any other spelling decodes
// as-is and is rejected by Valid, so lint can name it.
type ScanEnabled string

// The ScanEnabled values. The zero value means auto.
const (
	ScanAuto ScanEnabled = "auto"
	ScanOn   ScanEnabled = "true"
	ScanOff  ScanEnabled = "false"
)

// UnmarshalYAML accepts a bool (enabled: true) or a string (enabled: auto).
func (e *ScanEnabled) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("line %d: scan.enabled must be auto, true or false", node.Line)
	}
	var v any
	if err := node.Decode(&v); err != nil {
		return err
	}
	parsed, err := ParseScanEnabled(v)
	if err != nil {
		return fmt.Errorf("line %d: %w", node.Line, err)
	}
	*e = parsed
	return nil
}

// ParseScanEnabled converts a decoded bool or string into a ScanEnabled.
// Strings pass through unvalidated (Valid checks them); other types fail.
func ParseScanEnabled(v any) (ScanEnabled, error) {
	switch t := v.(type) {
	case nil:
		return "", nil
	case bool:
		if t {
			return ScanOn, nil
		}
		return ScanOff, nil
	case string:
		return ScanEnabled(t), nil
	default:
		return "", fmt.Errorf("scan.enabled must be auto, true or false (got %v)", v)
	}
}

// Effective resolves the default (auto).
func (e ScanEnabled) Effective() ScanEnabled {
	if e == "" {
		return ScanAuto
	}
	return e
}

// Valid reports whether e is auto, true, false or unset.
func (e ScanEnabled) Valid() bool {
	switch e.Effective() {
	case ScanAuto, ScanOn, ScanOff:
		return true
	}
	return false
}

// ScanOverride is an entry's scan block — a chart or artifact manifest's,
// or a build entry's. Every field is optional; unset fields inherit the
// config's scan block, then the built-in defaults.
type ScanOverride struct {
	// Enabled is auto (default), true or false. auto defers to the
	// config's scan.enabled, then to the kind's own rule; true and false
	// override scan.enabled for this entry.
	Enabled       ScanEnabled     `yaml:"enabled"`
	FailOn        []string        `yaml:"failOn"`
	IgnoreUnfixed *bool           `yaml:"ignoreUnfixed"`
	Allowlist     AllowlistPolicy `yaml:"allowlist"`
}

// Built-in scan defaults, applied when neither the entry nor the config
// sets a field.
const (
	defaultAllowlistMaxDays = 90
	defaultAllowlistNewDays = 90
)

// defaultFailOn is the built-in blocking severity set.
var defaultFailOn = []string{"CRITICAL", "HIGH"}

// ScanPolicy is the resolved (entry over config over built-in) policy.
type ScanPolicy struct {
	// Enabled is false when scanning is off for the entry: an explicit
	// enabled: false, or auto under a config-level scan.enabled: false.
	Enabled bool
	// Auto reports that Enabled came from auto, so the kind's own rule
	// applies on top (an artifact scans only when it is a runnable image).
	Auto          bool
	FailOn        []string
	IgnoreUnfixed bool
	Allowlist     ResolvedAllowlist
}

// ResolvedAllowlist is the resolved allowlist policy.
type ResolvedAllowlist struct {
	MaxDays  int
	NewDays  int
	Generate bool
	Preamble string
}

// Validate checks the resolved horizon: both day counts positive, and new
// entries never stamped past what lint accepts.
func (a ResolvedAllowlist) Validate() error {
	if a.MaxDays <= 0 {
		return fmt.Errorf("scan.allowlist.maxDays must be positive (got %d)", a.MaxDays)
	}
	if a.NewDays <= 0 {
		return fmt.Errorf("scan.allowlist.newDays must be positive (got %d)", a.NewDays)
	}
	if a.NewDays > a.MaxDays {
		return fmt.Errorf("scan.allowlist.newDays (%d) exceeds scan.allowlist.maxDays (%d)", a.NewDays, a.MaxDays)
	}
	return nil
}

// EffectiveScanPolicy resolves an entry's scan block against the config's:
// each field from the entry when set, else from the config, else the
// built-in default.
func EffectiveScanPolicy(global Scan, o ScanOverride) ScanPolicy {
	p := ScanPolicy{
		FailOn:        slices.Clone(defaultFailOn),
		IgnoreUnfixed: true,
		Allowlist: ResolvedAllowlist{
			MaxDays:  pick(o.Allowlist.MaxDays, global.Allowlist.MaxDays, defaultAllowlistMaxDays),
			NewDays:  pick(o.Allowlist.NewDays, global.Allowlist.NewDays, defaultAllowlistNewDays),
			Generate: pick(o.Allowlist.Generate, global.Allowlist.Generate, false),
			Preamble: pick(o.Allowlist.Preamble, global.Allowlist.Preamble, ""),
		},
	}
	switch {
	case len(o.FailOn) > 0:
		p.FailOn = o.FailOn
	case len(global.FailOn) > 0:
		p.FailOn = global.FailOn
	}
	p.IgnoreUnfixed = pick(o.IgnoreUnfixed, global.IgnoreUnfixed, true)
	switch o.Enabled.Effective() {
	case ScanOn:
		p.Enabled = true
	case ScanOff:
		p.Enabled = false
	default:
		p.Enabled = global.EffectiveEnabled()
		p.Auto = true
	}
	return p
}

// pick returns the first set pointer's value, else def.
func pick[T any](entry, global *T, def T) T {
	if entry != nil {
		return *entry
	}
	if global != nil {
		return *global
	}
	return def
}

// HasImageFields reports whether the override sets a field that only an
// image scan consumes (failOn, ignoreUnfixed, allowlist) — meaningless on
// an entry with no images to scan.
func (o ScanOverride) HasImageFields() bool {
	return len(o.FailOn) > 0 || o.IgnoreUnfixed != nil || o.Allowlist.IsSet()
}
