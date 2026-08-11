// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package sign

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/sigstore/cosign/v3/cmd/cosign/cli/options"
	clisign "github.com/sigstore/cosign/v3/cmd/cosign/cli/sign"
	"github.com/sigstore/cosign/v3/cmd/cosign/cli/signcommon"

	// Register the KMS URI schemes the signing config may name.
	_ "github.com/sigstore/sigstore/pkg/signature/kms/aws"
	_ "github.com/sigstore/sigstore/pkg/signature/kms/azure"
	_ "github.com/sigstore/sigstore/pkg/signature/kms/gcp"

	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
)

// signTimeout bounds one signing operation (OIDC exchange, Fulcio, Rekor,
// registry writes included).
const signTimeout = 3 * time.Minute

// Signer signs OCI artifacts per one resolved signing config.
type Signer struct {
	signing  *spec.Signing
	keychain authn.Keychain
	// keyRef and passFunc override the key for tests (a local cosign
	// keypair instead of keyless/KMS).
	keyRef     string
	passBytes  []byte
	ignoreTlog bool
}

// New builds a signer for a resolved signing config.
func New(signing *spec.Signing, keychain authn.Keychain) (*Signer, error) {
	switch signing.Provider {
	case "keyless":
	case "kms":
		if signing.KMS == nil || signing.KMS.Key == "" {
			return nil, errors.New("kms signing config lacks key")
		}
	default:
		return nil, fmt.Errorf("unsupported signing provider %q", signing.Provider)
	}
	return &Signer{signing: signing, keychain: keychain}, nil
}

// NewWithKeyFile builds a test signer over a local cosign keypair, offline
// (no transparency log).
func NewWithKeyFile(keyPath string, password []byte, keychain authn.Keychain) *Signer {
	return &Signer{
		signing:    &spec.Signing{Provider: "kms"},
		keychain:   keychain,
		keyRef:     keyPath,
		passBytes:  password,
		ignoreTlog: true,
	}
}

// Sign signs ref (a digest reference), attaching the signature as a
// sigstore bundle via the OCI referrers API. recursive also signs a
// manifest list's children.
func (s *Signer) Sign(ctx context.Context, ref string, recursive bool) error {
	ro := &options.RootOptions{Timeout: signTimeout}
	ko := options.KeyOpts{
		SkipConfirmation: true,
		PassFunc:         func(bool) ([]byte, error) { return s.passBytes, nil },
	}
	tlogUpload := false
	switch {
	case s.keyRef != "":
		ko.KeyRef = s.keyRef
	case s.signing.Provider == "kms":
		ko.KeyRef = s.signing.KMS.Key
	default:
		// Keyless: ambient OIDC (the CI workflow identity) through the
		// public trust services, pinned deliberately — public repos want
		// the public log, and the storage format is bundles either way.
		ko.FulcioURL = options.DefaultFulcioURL
		ko.RekorURL = options.DefaultRekorURL
		ko.OIDCIssuer = options.DefaultOIDCIssuerURL
		tlogUpload = s.signing.Keyless.TlogUploadEnabled()
	}
	if s.ignoreTlog {
		tlogUpload = false
	}

	signOpts := options.SignOptions{
		Upload:           true,
		Recursive:        recursive,
		SkipConfirmation: true,
		TlogUpload:       tlogUpload,
		NewBundleFormat:  true,
		UseSigningConfig: false,
	}
	if err := signcommon.LoadTrustedMaterialAndSigningConfig(ctx, &ko, false, "",
		ko.RekorURL, ko.FulcioURL, ko.OIDCIssuer, "", "",
		tlogUpload, true, "", ko.KeyRef, false, "", "", "", "", "", ""); err != nil {
		return fmt.Errorf("loading signing trust material: %w", err)
	}
	if err := clisign.SignCmd(ctx, ro, ko, signOpts, []string{ref}); err != nil {
		return fmt.Errorf("sign %s: %w", ref, err)
	}
	return nil
}
