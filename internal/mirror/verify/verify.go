// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package verify

import (
	"context"
	"crypto"
	"errors"
	"fmt"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/sigstore/cosign/v3/cmd/cosign/cli/options"
	cliverify "github.com/sigstore/cosign/v3/cmd/cosign/cli/verify"
	"github.com/sigstore/cosign/v3/pkg/cosign"
	ociremote "github.com/sigstore/cosign/v3/pkg/oci/remote"

	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
)

// Subject is one verification: what to check and against which trust.
type Subject struct {
	// Ref is the artifact to verify (tag or digest reference).
	Ref string
	// Keychain authenticates registry reads (nil: ambient default).
	Keychain authn.Keychain

	// KeyRef verifies against a public key (a path, URL or KMS URI);
	// empty means keyless.
	KeyRef string
	// HashAlgorithm overrides the payload digest for key verification
	// (some publishers sign with sha512).
	HashAlgorithm crypto.Hash
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
}

// UpstreamSubject translates a manifest verifyUpstream rule for ref.
// Provider none is the caller's business — it is a documented gap, never a
// verification.
func UpstreamSubject(ref string, rule spec.VerifyRule, keychain authn.Keychain) (Subject, error) {
	s := Subject{Ref: ref, Keychain: keychain, SignatureRepository: rule.SignatureRepository}
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
			s.HashAlgorithm = crypto.SHA256
		case "sha384":
			s.HashAlgorithm = crypto.SHA384
		case "sha512":
			s.HashAlgorithm = crypto.SHA512
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
func MirrorSubject(ref string, signing *spec.Signing, keychain authn.Keychain) (Subject, error) {
	s := Subject{Ref: ref, Keychain: keychain}
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
		s.HashAlgorithm = crypto.SHA256
		s.IgnoreTlog = true
	default:
		return Subject{}, fmt.Errorf("unsupported signing provider %q", signing.Provider)
	}
	return s, nil
}

// Verify checks the subject, driving the same discovery the cosign CLI
// uses: sigstore bundles via referrers when present, legacy .sig tags
// otherwise.
func Verify(ctx context.Context, s Subject) error {
	// Defense in depth behind the Subject constructors: cosign treats an
	// identity with neither subject nor regexp as an unconstrained match,
	// so an empty keyless subject would verify against ANY signer.
	if s.KeyRef == "" && s.Identity == "" && s.IdentityRegexp == "" {
		return fmt.Errorf("verify %s: keyless verification requires a certificate identity or identity regexp", s.Ref)
	}
	ref, err := name.ParseReference(s.Ref)
	if err != nil {
		return fmt.Errorf("parse %s: %w", s.Ref, err)
	}
	keychain := s.Keychain
	if keychain == nil {
		keychain = authn.DefaultKeychain
	}
	regOpts := []ociremote.Option{
		ociremote.WithRemoteOptions(remote.WithContext(ctx), remote.WithAuthFromKeychain(keychain)),
	}
	if s.SignatureRepository != "" {
		sigRepo, err := name.NewRepository(s.SignatureRepository)
		if err != nil {
			return fmt.Errorf("parse signature repository %q: %w", s.SignatureRepository, err)
		}
		regOpts = append(regOpts, ociremote.WithTargetRepository(sigRepo))
	}

	co := &cosign.CheckOpts{
		RegistryClientOpts: regOpts,
		IgnoreTlog:         s.IgnoreTlog,
		MaxWorkers:         10,
	}
	if s.KeyRef == "" {
		id := cosign.Identity{Issuer: s.Issuer}
		if s.Identity != "" {
			id.Subject = s.Identity
		} else {
			id.SubjectRegExp = s.IdentityRegexp
		}
		co.Identities = []cosign.Identity{id}
	}

	// Prefer bundles attached via the referrers API; fall back to the
	// legacy signature tag when the artifact carries none.
	bundles, _, err := cosign.GetBundles(ctx, ref, co.RegistryClientOpts)
	co.NewBundleFormat = err == nil && len(bundles) > 0

	offlineKey := s.KeyRef != "" && co.IgnoreTlog
	if err := cliverify.SetTrustedMaterial(ctx, "", "", "", "", "", offlineKey, co); err != nil {
		return fmt.Errorf("setting trusted material: %w", err)
	}
	keyless := s.KeyRef == ""
	if err := cliverify.SetLegacyClientsAndKeys(ctx, co.IgnoreTlog, keyless, keyless,
		options.DefaultRekorURL, "", "", "", "", co); err != nil {
		return fmt.Errorf("setting up clients and keys: %w", err)
	}
	hash := s.HashAlgorithm
	if hash == 0 {
		hash = crypto.SHA256
	}
	sigVerifier, _, closeSV, err := cliverify.LoadVerifierFromKeyOrCert(ctx, s.KeyRef, "", "", "", hash, false, false, co)
	if err != nil {
		return fmt.Errorf("loading verifier: %w", err)
	}
	defer closeSV()
	co.SigVerifier = sigVerifier

	if co.NewBundleFormat {
		_, _, err = cosign.VerifyImageAttestations(ctx, ref, co)
	} else {
		_, _, err = cosign.VerifyImageSignatures(ctx, ref, co)
	}
	if err != nil {
		return fmt.Errorf("verify %s: %w", s.Ref, err)
	}
	return nil
}
