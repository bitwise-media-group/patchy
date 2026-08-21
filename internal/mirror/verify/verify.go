// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package verify

import (
	"context"
	"errors"
	"fmt"

	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
)

// ToolRunner executes the cosign binary; a seam so tests can can its
// invocations. scan.ExecRunner satisfies it.
type ToolRunner interface {
	// Run executes name with args and extra environment, returning stdout.
	Run(ctx context.Context, name string, args, env []string) (stdout []byte, err error)
	// Look reports whether the binary is available.
	Look(name string) bool
}

// Subject is one verification: what to check and against which trust.
type Subject struct {
	// Ref is the artifact to verify (tag or digest reference).
	Ref string

	// KeyRef verifies against a public key (a path, URL or KMS URI);
	// empty means keyless.
	KeyRef string
	// HashAlgorithm overrides the payload digest for key verification
	// ("", "sha256", "sha384" or "sha512" — some publishers sign with
	// sha512).
	HashAlgorithm string
	// IdentityRegexp matches the certificate identity for keyless
	// verification (mutually exclusive with Identity).
	IdentityRegexp string
	// Identity pins the exact certificate identity (the mirror's own
	// signatures).
	Identity string
	// Issuer is the OIDC issuer for keyless verification.
	Issuer string
	// SignatureRepository reads signatures from a dedicated repository
	// instead of alongside the artifact.
	SignatureRepository string
	// IgnoreTlog skips transparency-log verification (key-based rules
	// and offline self-checks).
	IgnoreTlog bool
	// AllowHTTPRegistry permits plain-HTTP registries (tests, local
	// mirrors).
	AllowHTTPRegistry bool
}

// UpstreamSubject translates a manifest verifyUpstream rule for ref.
// Provider none is the caller's business — it is a documented gap, never a
// verification.
func UpstreamSubject(ref string, rule spec.VerifyRule) (Subject, error) {
	s := Subject{Ref: ref, SignatureRepository: rule.SignatureRepository}
	switch rule.EffectiveProvider() {
	case "cosign-keyless":
		if rule.CertificateIdentityRegexp == "" {
			return Subject{}, errors.New("cosign-keyless requires certificateIdentityRegexp")
		}
		s.IdentityRegexp = rule.CertificateIdentityRegexp
		s.Issuer = rule.EffectiveOidcIssuer()
	case "cosign-key":
		if rule.Key == "" {
			return Subject{}, errors.New("cosign-key requires key")
		}
		s.KeyRef = rule.Key
		s.IgnoreTlog = true
		switch rule.SignatureDigestAlgorithm {
		case "", "sha256":
			s.HashAlgorithm = "sha256"
		case "sha384":
			s.HashAlgorithm = "sha384"
		case "sha512":
			s.HashAlgorithm = "sha512"
		default:
			return Subject{}, fmt.Errorf("unsupported signatureDigestAlgorithm %q", rule.SignatureDigestAlgorithm)
		}
	default:
		return Subject{}, fmt.Errorf("unsupported provider %q", rule.Provider)
	}
	return s, nil
}

// MirrorSubject translates the mirror's own signing config into the
// self-verification for ref: the exact identity keyless signing produces,
// or the KMS key's public half.
func MirrorSubject(ref string, signing *spec.Signing) (Subject, error) {
	s := Subject{Ref: ref}
	switch signing.Provider {
	case "keyless":
		if signing.Keyless == nil || signing.Keyless.CertificateIdentity == "" {
			return Subject{}, errors.New("keyless signing config lacks certificateIdentity")
		}
		s.Identity = signing.Keyless.CertificateIdentity
		s.Issuer = signing.Keyless.CertificateOidcIssuer
		if s.Issuer == "" {
			s.Issuer = "https://token.actions.githubusercontent.com"
		}
	case "kms":
		if signing.KMS == nil || signing.KMS.Key == "" {
			return Subject{}, errors.New("kms signing config lacks key")
		}
		s.KeyRef = signing.KMS.Key
		s.HashAlgorithm = "sha256"
		s.IgnoreTlog = true
	default:
		return Subject{}, fmt.Errorf("unsupported signing provider %q", signing.Provider)
	}
	return s, nil
}

// Verify checks the subject by shelling out to the cosign binary, whose
// default --new-bundle-format=true drives the same discovery the old
// in-process path did: sigstore bundles via referrers when present, legacy
// .sig tags otherwise. The CLI additionally checks claims (--check-claims
// defaults true) — strictly stronger than the previous nil ClaimVerifier,
// and desirable.
func Verify(ctx context.Context, runner ToolRunner, s Subject) error {
	// Defense in depth behind the Subject constructors: cosign treats an
	// identity with neither subject nor regexp as an unconstrained match,
	// so an empty keyless subject would verify against ANY signer.
	if s.KeyRef == "" && s.Identity == "" && s.IdentityRegexp == "" {
		return fmt.Errorf("verify %s: keyless verification requires a certificate identity or identity regexp", s.Ref)
	}
	if !runner.Look("cosign") {
		return fmt.Errorf("verify %s: cosign not found on PATH (install it: mise install / brew install cosign)", s.Ref)
	}

	args := []string{"verify"}
	if s.KeyRef != "" {
		alg := s.HashAlgorithm
		if alg == "" {
			alg = "sha256"
		}
		args = append(args, "--key", s.KeyRef, "--signature-digest-algorithm="+alg)
	} else {
		if s.Identity != "" {
			args = append(args, "--certificate-identity="+s.Identity)
		} else {
			args = append(args, "--certificate-identity-regexp="+s.IdentityRegexp)
		}
		args = append(args, "--certificate-oidc-issuer="+s.Issuer)
	}
	if s.IgnoreTlog {
		args = append(args, "--insecure-ignore-tlog=true")
	}
	if s.AllowHTTPRegistry {
		args = append(args, "--allow-insecure-registry")
	}
	args = append(args, s.Ref)

	var env []string
	if s.SignatureRepository != "" {
		env = append(env, "COSIGN_REPOSITORY="+s.SignatureRepository)
	}
	// Stdout (the verified-payload JSON) is discarded; on failure the
	// runner folds stderr into the error, which is cosign's failure shape.
	if _, err := runner.Run(ctx, "cosign", args, env); err != nil {
		return fmt.Errorf("verify %s: %w", s.Ref, err)
	}
	return nil
}
