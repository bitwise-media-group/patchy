// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package sign

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
)

// signTimeout bounds one signing operation (OIDC exchange, Fulcio, Rekor,
// registry writes included).
const signTimeout = 3 * time.Minute

// The public sigstore trust services keyless signing pins deliberately —
// public repos want the public log. Each entry mirrors the public-good
// instance's own signing config (operator, API version, validity start),
// minus the TSA: the tlog entry is the signing-time evidence.
var (
	fulcioService = signingService{
		URL: "https://fulcio.sigstore.dev", MajorAPIVersion: 1,
		ValidFor: serviceValidity{Start: "2022-04-13T20:06:15Z"}, Operator: "sigstore.dev",
	}
	oidcService = signingService{
		URL: "https://oauth2.sigstore.dev/auth", MajorAPIVersion: 1,
		ValidFor: serviceValidity{Start: "2022-04-13T20:06:15Z"}, Operator: "sigstore.dev",
	}
	rekorService = signingService{
		URL: "https://rekor.sigstore.dev", MajorAPIVersion: 1,
		ValidFor: serviceValidity{Start: "2021-01-12T11:53:27Z"}, Operator: "sigstore.dev",
	}
)

// signingConfigMediaType is the sigstore signing config schema cosign v3
// reads through --signing-config.
const signingConfigMediaType = "application/vnd.dev.sigstore.signingconfig.v0.2+json"

// signingConfig is the sigstore protobuf-specs SigningConfig, JSON-encoded:
// the service set one cosign sign run may use. A service list left empty
// is a service never contacted.
type signingConfig struct {
	MediaType       string           `json:"mediaType"`
	CAURLs          []signingService `json:"caUrls,omitempty"`
	OIDCURLs        []signingService `json:"oidcUrls,omitempty"`
	RekorTlogURLs   []signingService `json:"rekorTlogUrls,omitempty"`
	RekorTlogConfig serviceSelector  `json:"rekorTlogConfig"`
	TSAConfig       serviceSelector  `json:"tsaConfig"`
}

type signingService struct {
	URL             string          `json:"url"`
	MajorAPIVersion int             `json:"majorApiVersion"`
	ValidFor        serviceValidity `json:"validFor"`
	Operator        string          `json:"operator"`
}

type serviceValidity struct {
	Start string `json:"start"`
}

type serviceSelector struct {
	Selector string `json:"selector,omitempty"`
}

// ToolRunner executes the cosign binary; a seam so tests can can its
// invocations. scan.ExecRunner satisfies it.
type ToolRunner interface {
	// Run executes name with args and extra environment, returning stdout.
	Run(ctx context.Context, name string, args, env []string) (stdout []byte, err error)
	// Look reports whether the binary is available.
	Look(name string) bool
}

// Signer signs OCI artifacts per one resolved signing config by shelling
// out to the cosign binary. KMS URI schemes (awskms:// gcpkms://
// azurekms:// hashivault://) are whatever the installed cosign build
// supports — upstream release binaries include all of them.
type Signer struct {
	signing *spec.Signing
	runner  ToolRunner
	// keyRef and password override the key for tests (a local cosign
	// keypair instead of keyless/KMS).
	keyRef        string
	password      []byte
	allowInsecure bool
}

// New builds a signer for a resolved signing config.
func New(signing *spec.Signing, runner ToolRunner) (*Signer, error) {
	switch signing.Provider {
	case "keyless":
	case "kms":
		if signing.KMS == nil || signing.KMS.Key == "" {
			return nil, errors.New("kms signing config lacks key")
		}
	default:
		return nil, fmt.Errorf("unsupported signing provider %q", signing.Provider)
	}
	return &Signer{signing: signing, runner: runner}, nil
}

// NewWithKeyFile builds a test signer over a local cosign keypair, offline
// (no transparency log, plain-HTTP registries allowed).
func NewWithKeyFile(keyPath string, password []byte, runner ToolRunner) *Signer {
	return &Signer{
		signing:       &spec.Signing{Provider: "kms"},
		runner:        runner,
		keyRef:        keyPath,
		password:      password,
		allowInsecure: true,
	}
}

// Sign signs ref (a digest reference), attaching the signature as a
// sigstore bundle via the OCI referrers API. recursive also signs a
// manifest list's children.
func (s *Signer) Sign(ctx context.Context, ref string, recursive bool) error {
	if !s.runner.Look("cosign") {
		return fmt.Errorf("sign %s: cosign not found on PATH (install it: mise install / brew install cosign)", ref)
	}
	ctx, cancel := context.WithTimeout(ctx, signTimeout)
	defer cancel()

	// Every service comes from the signing config, never per-service flags
	// (deprecated in cosign v3). A key signs with none — no Fulcio, no
	// tlog, no TSA — so its bundle carries the signature alone.
	cfg := signingConfig{MediaType: signingConfigMediaType}
	var keyArgs, env []string
	switch {
	case s.keyRef != "":
		keyArgs = []string{"--key", s.keyRef}
		env = append(env, "COSIGN_PASSWORD="+string(s.password))
	case s.signing.Provider == "kms":
		keyArgs = []string{"--key", s.signing.KMS.Key}
	default:
		// Keyless: ambient OIDC (the CI workflow identity) through the
		// pinned public trust services.
		cfg.CAURLs = []signingService{fulcioService}
		cfg.OIDCURLs = []signingService{oidcService}
		if s.signing.Keyless.TlogUploadEnabled() {
			cfg.RekorTlogURLs = []signingService{rekorService}
			cfg.RekorTlogConfig.Selector = "ANY"
		}
	}
	cfgPath, err := writeSigningConfig(cfg)
	if err != nil {
		return fmt.Errorf("sign %s: %w", ref, err)
	}
	defer func() { _ = os.Remove(cfgPath) }()

	// --signing-config also pins the storage format: cosign accepts it only
	// for sigstore bundles, which it attaches as OCI 1.1 referrers — never
	// sha256-<digest>.sig tags — so a cosign that would write legacy tags
	// fails instead. (A registry without the referrers API gets the spec's
	// sha256-<digest> fallback index tag; ECR serves the API.)
	args := []string{
		"sign", "--yes",
		"--recursive=" + strconv.FormatBool(recursive),
		"--signing-config=" + cfgPath,
	}
	args = append(args, keyArgs...)
	if s.allowInsecure {
		args = append(args, "--allow-insecure-registry")
	}
	args = append(args, ref)

	if _, err := s.runner.Run(ctx, "cosign", args, env); err != nil {
		return fmt.Errorf("sign %s: %w", ref, err)
	}
	return nil
}

// writeSigningConfig writes cfg to a fresh temp file for cosign to read.
func writeSigningConfig(cfg signingConfig) (string, error) {
	data, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("encode signing config: %w", err)
	}
	f, err := os.CreateTemp("", "patchy-signing-config-*.json")
	if err != nil {
		return "", fmt.Errorf("write signing config: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("write signing config: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("write signing config: %w", err)
	}
	return f.Name(), nil
}
