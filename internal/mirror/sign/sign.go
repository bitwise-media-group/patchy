// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package sign

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
)

// signTimeout bounds one signing operation (OIDC exchange, Fulcio, Rekor,
// registry writes included).
const signTimeout = 3 * time.Minute

// The public sigstore trust services keyless signing pins deliberately —
// public repos want the public log, and the storage format is bundles
// either way.
const (
	defaultFulcioURL     = "https://fulcio.sigstore.dev"
	defaultRekorURL      = "https://rekor.sigstore.dev"
	defaultOIDCIssuerURL = "https://oauth2.sigstore.dev/auth"
)

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
	ignoreTlog    bool
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
		ignoreTlog:    true,
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

	tlogUpload := false
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
		keyArgs = []string{
			"--fulcio-url=" + defaultFulcioURL,
			"--rekor-url=" + defaultRekorURL,
			"--oidc-issuer=" + defaultOIDCIssuerURL,
		}
		tlogUpload = s.signing.Keyless.TlogUploadEnabled()
	}
	if s.ignoreTlog {
		tlogUpload = false
	}

	// --new-bundle-format is deprecated-but-live in cosign v3 and pins
	// bundle storage on older v2.5–2.x installs; drop it when cosign
	// removes the flag. --use-signing-config=false keeps the pinned
	// service URLs authoritative.
	args := []string{
		"sign", "--yes",
		"--recursive=" + strconv.FormatBool(recursive),
		"--new-bundle-format=true",
		"--use-signing-config=false",
		"--tlog-upload=" + strconv.FormatBool(tlogUpload),
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
