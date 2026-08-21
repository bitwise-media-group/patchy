// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package spec

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"
)

// APIVersion is the schema identity every mirror file declares.
const APIVersion = "mirror.patchy.bitwisemedia.uk/v1alpha1"

// The kinds the schema defines.
const (
	KindMirrorConfig = "MirrorConfig"
	KindChart        = "Chart"
	KindArtifact     = "Artifact"
)

// ConfigFile is the name of the root configuration file; its directory is
// the mirror root.
const ConfigFile = "mirror.yaml"

// Config is mirror.yaml: pipeline-wide defaults. Per-entry intent lives in
// each entry's manifest.yaml, never here.
type Config struct {
	APIVersion string   `yaml:"apiVersion"`
	Kind       string   `yaml:"kind"`
	Registry   Registry `yaml:"registry"`
	Signing    Signing  `yaml:"signing"`
	Update     Update   `yaml:"update"`
	Scan       Scan     `yaml:"scan"`
	// SourceRegistryRewrites reroutes pulls from an upstream registry host
	// (e.g. a pull-through cache in front of docker.io). Recorded sources
	// and publish targets keep the canonical path.
	SourceRegistryRewrites map[string]string `yaml:"sourceRegistryRewrites"`
}

// Registry locates the mirror: one registry URL with path namespaces for
// charts, images and artifacts beneath it.
type Registry struct {
	URL string `yaml:"url"`
	// ChartNamespace prefixes published charts (default "charts").
	ChartNamespace string `yaml:"chartNamespace"`
	// ImageNamespace prefixes mirrored images by their canonical source
	// path (default "images"): <url>/<imageNamespace>/<source path>:<tag>.
	ImageNamespace string `yaml:"imageNamespace"`
	// ArtifactNamespace prefixes mirrored OCI artifacts (default "artifacts").
	ArtifactNamespace string `yaml:"artifactNamespace"`
}

// Signing configures how published OCI artifacts are signed: keyless
// (ambient OIDC, Fulcio certificate, transparency log) or a KMS key. The
// top-level block is the default; a per-entry signing block replaces it
// wholesale — the two are never merged field-by-field.
type Signing struct {
	// Provider is "keyless" or "kms".
	Provider string          `yaml:"provider"`
	Keyless  *KeylessSigning `yaml:"keyless"`
	KMS      *KMSSigning     `yaml:"kms"`
}

// KeylessSigning pins the certificate identity the mirror's own signatures
// carry — used both to sign and to self-verify for publish idempotency.
type KeylessSigning struct {
	CertificateIdentity   string `yaml:"certificateIdentity"`
	CertificateOidcIssuer string `yaml:"certificateOidcIssuer"`
	// TlogUpload records signatures in the public transparency log
	// (default true; public repos want the log).
	TlogUpload *bool `yaml:"tlogUpload"`
}

// TlogUploadEnabled resolves the TlogUpload default.
func (k *KeylessSigning) TlogUploadEnabled() bool {
	return k == nil || k.TlogUpload == nil || *k.TlogUpload
}

// KMSSigning signs with a cloud KMS key.
type KMSSigning struct {
	// Key is a KMS URI: gcpkms://, awskms:// or azurekms://.
	Key string `yaml:"key"`
}

// Update tunes the tracked-tag soak window.
type Update struct {
	// CooldownDays is how long a tag must have been published before a
	// tracked-image pick may adopt it (default 3): a tag published this
	// recently can still be yanked or hot-fixed, and the mirror should
	// not be what discovers that. Kept short on purpose — every day here
	// is a day the platform knowingly runs behind on security patches.
	CooldownDays *int `yaml:"cooldownDays"`
}

// defaultCooldownDays is the soak window applied when mirror.yaml sets none.
const defaultCooldownDays = 3

// EffectiveCooldownDays resolves the default.
func (u Update) EffectiveCooldownDays() int {
	if u.CooldownDays != nil {
		return *u.CooldownDays
	}
	return defaultCooldownDays
}

// Scan is the global vulnerability-scan policy. FailOn and IgnoreUnfixed
// can be overridden per entry; the scanner roster is global.
type Scan struct {
	// Enabled turns image scanning off entirely when false (default
	// true). With scanning enabled, at least one image scanner must be
	// enabled in Scanners.
	Enabled  *bool    `yaml:"enabled"`
	Scanners Scanners `yaml:"scanners"`
	// FailOn lists blocking severities (default CRITICAL, HIGH).
	FailOn []string `yaml:"failOn"`
	// IgnoreUnfixed skips findings with no fixed version (default true).
	IgnoreUnfixed *bool `yaml:"ignoreUnfixed"`
	// AllowlistMaxDays bounds every allowlist entry's expired_at horizon
	// (default 90) — expiry forces re-review instead of accepted-forever CVEs.
	AllowlistMaxDays int `yaml:"allowlistMaxDays"`
	// AllowlistNewDays is the expiry stamped on new derived entries
	// (default 90). Surviving entries keep the date they were first
	// accepted with, so regeneration never rolls the clock forward.
	AllowlistNewDays int `yaml:"allowlistNewDays"`
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

// Defaults for the scan policy.
const (
	defaultAllowlistMaxDays = 90
	defaultAllowlistNewDays = 90
)

// EffectiveFailOn resolves the FailOn default.
func (s Scan) EffectiveFailOn() []string {
	if len(s.FailOn) > 0 {
		return s.FailOn
	}
	return []string{"CRITICAL", "HIGH"}
}

// EffectiveEnabled resolves the scanning-enabled default (on).
func (s Scan) EffectiveEnabled() bool {
	return s.Enabled == nil || *s.Enabled
}

// EffectiveIgnoreUnfixed resolves the IgnoreUnfixed default.
func (s Scan) EffectiveIgnoreUnfixed() bool {
	return s.IgnoreUnfixed == nil || *s.IgnoreUnfixed
}

// EffectiveAllowlistMaxDays resolves the horizon default.
func (s Scan) EffectiveAllowlistMaxDays() int {
	if s.AllowlistMaxDays > 0 {
		return s.AllowlistMaxDays
	}
	return defaultAllowlistMaxDays
}

// EffectiveAllowlistNewDays resolves the new-entry default.
func (s Scan) EffectiveAllowlistNewDays() int {
	if s.AllowlistNewDays > 0 {
		return s.AllowlistNewDays
	}
	return defaultAllowlistNewDays
}

// LoadConfig reads and validates the mirror.yaml at path.
func LoadConfig(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var c Config
	if err := decodeStrict(raw, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := checkHeader(c.APIVersion, c.Kind, KindMirrorConfig); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if c.Registry.URL == "" {
		return nil, fmt.Errorf("%s: registry.url is required", path)
	}
	if c.Registry.ChartNamespace == "" {
		c.Registry.ChartNamespace = "charts"
	}
	if c.Registry.ImageNamespace == "" {
		c.Registry.ImageNamespace = "images"
	}
	if c.Registry.ArtifactNamespace == "" {
		c.Registry.ArtifactNamespace = "artifacts"
	}
	if c.Signing.Provider == "" {
		c.Signing.Provider = "keyless"
	}
	if err := validateSigning(&c.Signing); err != nil {
		return nil, fmt.Errorf("%s: signing: %w", path, err)
	}
	if c.Scan.EffectiveAllowlistNewDays() > c.Scan.EffectiveAllowlistMaxDays() {
		return nil, fmt.Errorf("%s: scan.allowlistNewDays (%d) exceeds scan.allowlistMaxDays (%d)",
			path, c.Scan.EffectiveAllowlistNewDays(), c.Scan.EffectiveAllowlistMaxDays())
	}
	return &c, nil
}

// validateSigning checks one signing block (global or per-entry override).
func validateSigning(s *Signing) error {
	switch s.Provider {
	case "keyless":
		return nil
	case "kms":
		if s.KMS == nil || s.KMS.Key == "" {
			return errors.New("provider kms requires kms.key")
		}
		return nil
	case "":
		return errors.New("provider is required (keyless or kms)")
	default:
		return fmt.Errorf("unknown provider %q (want keyless or kms)", s.Provider)
	}
}

// ErrNoRoot reports that no mirror.yaml exists in the directory or any
// parent — the one FindRoot failure "scaffold a fresh store" may answer.
var ErrNoRoot = errors.New("no " + ConfigFile + " found")

// FindRoot walks up from dir to the directory holding mirror.yaml.
func FindRoot(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for d := abs; ; {
		if info, err := os.Stat(filepath.Join(d, ConfigFile)); err == nil && !info.IsDir() {
			return d, nil
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", fmt.Errorf("%w in %s or any parent directory", ErrNoRoot, abs)
		}
		d = parent
	}
}

// decodeStrict parses YAML rejecting unknown fields.
func decodeStrict(raw []byte, out any) error {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		return err
	}
	// A second document would be silently dropped; reject it.
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("expected a single YAML document")
	}
	return nil
}

// checkHeader validates the apiVersion/kind pair every mirror file carries.
func checkHeader(apiVersion, kind, wantKind string) error {
	if apiVersion != APIVersion {
		return fmt.Errorf("apiVersion %q is not %s", apiVersion, APIVersion)
	}
	if kind != wantKind {
		return fmt.Errorf("kind %q is not %s", kind, wantKind)
	}
	return nil
}
