// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package spec

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v4"
)

// APIVersion is the schema identity every mirror manifest declares.
const APIVersion = "mirror.patchy.bitwisemedia.uk/v1alpha1"

// The manifest kinds the schema defines.
const (
	KindChart    = "Chart"
	KindArtifact = "Artifact"
)

// Config is the mirror's view of the patchy configuration: the shared
// registries, signing and scan blocks plus the mirror: block's update and
// sourceRegistryRewrites. The CLI assembles it from the layered patchy
// config; the engine never discovers or reads config files itself, so a
// Config is always handed in. Per-entry intent lives in each entry's
// manifest.yaml, never here.
type Config struct {
	Registries []Registry `yaml:"registries"`
	Signing    Signing    `yaml:"signing"`
	Update     Update     `yaml:"update"`
	Scan       Scan       `yaml:"scan"`
	// SourceRegistryRewrites reroutes pulls from an upstream registry host
	// (e.g. a pull-through cache in front of docker.io). Recorded sources
	// and publish targets keep the canonical path.
	SourceRegistryRewrites map[string]string `yaml:"sourceRegistryRewrites"`
}

// Resolve validates the config and fills its defaults in place: at least
// one registry, each with a well-formed unique name and a url, namespace
// defaults per registry, the signing default (keyless) and every signing
// block's shape, and the scan block's allowlist horizon. Callers that only
// need part of the config (the dev commands) never call it.
func (c *Config) Resolve() error {
	if len(c.Registries) == 0 {
		return errors.New("registries must list at least one registry")
	}
	return c.resolve()
}

// ResolveWithoutRegistries is Resolve for callers that never contact a
// registry (`patchy build --dry-run`): the same validation and defaults, but
// an empty registries list is allowed, so a plan needs no registry config.
func (c *Config) ResolveWithoutRegistries() error {
	return c.resolve()
}

// resolve is Resolve's body, after the registry-count check.
func (c *Config) resolve() error {
	seen := make(map[string]bool, len(c.Registries))
	for i := range c.Registries {
		r := &c.Registries[i]
		if !registryNameRe.MatchString(r.Name) {
			return fmt.Errorf("registries[%d]: name %q must match %s "+
				"(it keys lock targets and --registry)", i, r.Name, registryNameRe)
		}
		if seen[r.Name] {
			return fmt.Errorf("registries[%d]: duplicate name %q", i, r.Name)
		}
		seen[r.Name] = true
		if r.URL == "" {
			return fmt.Errorf("registries[%d] (%s): url is required", i, r.Name)
		}
		if r.ChartNamespace == "" {
			r.ChartNamespace = "charts"
		}
		if r.ImageNamespace == "" {
			r.ImageNamespace = "images"
		}
		if r.ArtifactNamespace == "" {
			r.ArtifactNamespace = "artifacts"
		}
		if r.Signing != nil {
			if err := validateSigning(r.Signing); err != nil {
				return fmt.Errorf("registries[%d] (%s): signing: %w", i, r.Name, err)
			}
		}
	}
	if c.Signing.Provider == "" {
		c.Signing.Provider = "keyless"
	}
	if err := validateSigning(&c.Signing); err != nil {
		return fmt.Errorf("signing: %w", err)
	}
	if err := EffectiveScanPolicy(c.Scan, ScanOverride{}).Allowlist.Validate(); err != nil {
		return err
	}
	return nil
}

// Registry locates one mirror destination: a registry URL with path
// namespaces for charts, images and artifacts beneath it. Every entry
// publishes to every configured registry.
type Registry struct {
	// Name is the registry's identity: the lock files' targets key and
	// the value `sync --registry` selects by. Lowercase alphanumerics and
	// hyphens.
	Name string `yaml:"name"`
	URL  string `yaml:"url"`
	// ChartNamespace prefixes published charts (default "charts").
	ChartNamespace string `yaml:"chartNamespace"`
	// ImageNamespace prefixes mirrored images by their canonical source
	// path (default "images"): <url>/<imageNamespace>/<source path>:<tag>.
	ImageNamespace string `yaml:"imageNamespace"`
	// ArtifactNamespace prefixes mirrored OCI artifacts (default "artifacts").
	ArtifactNamespace string `yaml:"artifactNamespace"`
	// Signing, when set, replaces the global signing block wholesale for
	// pushes to this registry (never merged field-by-field).
	Signing *Signing `yaml:"signing"`
}

// EffectiveSigning resolves the signing config for pushes to this registry:
// its own block when set, else the global default.
func (r Registry) EffectiveSigning(global *Signing) *Signing {
	if r.Signing != nil {
		return r.Signing
	}
	return global
}

// SelectRegistries resolves a set of registry names against the config, in
// declaration order. A nil or empty selection means every registry; an
// unknown name is an error.
func (c *Config) SelectRegistries(names []string) ([]Registry, error) {
	if len(names) == 0 {
		return c.Registries, nil
	}
	byName := make(map[string]Registry, len(c.Registries))
	for _, r := range c.Registries {
		byName[r.Name] = r
	}
	want := make(map[string]bool, len(names))
	for _, n := range names {
		if _, ok := byName[n]; !ok {
			return nil, fmt.Errorf("unknown registry %q (configured: %s)", n, registryNames(c.Registries))
		}
		want[n] = true
	}
	var out []Registry
	for _, r := range c.Registries {
		if want[r.Name] {
			out = append(out, r)
		}
	}
	return out, nil
}

// registryNames joins the configured registry names for error messages.
func registryNames(regs []Registry) string {
	names := make([]string, len(regs))
	for i, r := range regs {
		names[i] = r.Name
	}
	return strings.Join(names, ", ")
}

// Signing configures how published OCI artifacts are signed: keyless
// (ambient OIDC, Fulcio certificate, transparency log) or a KMS key. The
// top-level block is the default; a per-registry signing block replaces it
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

// defaultCooldownDays is the soak window applied when the config sets none.
const defaultCooldownDays = 3

// EffectiveCooldownDays resolves the default.
func (u Update) EffectiveCooldownDays() int {
	if u.CooldownDays != nil {
		return *u.CooldownDays
	}
	return defaultCooldownDays
}

// registryNameRe bounds registry names: they become lock-file map keys and
// --registry flag values, so no whitespace, colons or leading hyphens.
var registryNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// validateSigning checks one signing block (global or per-registry override).
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
