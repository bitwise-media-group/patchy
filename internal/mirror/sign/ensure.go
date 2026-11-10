// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package sign

import (
	"context"
	"errors"

	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
	"github.com/bitwise-media-group/patchy/internal/mirror/verify"
)

// ArtifactSigner signs one OCI artifact reference. *Signer satisfies it;
// tests inject key-file or recording signers.
type ArtifactSigner interface {
	Sign(ctx context.Context, ref string, recursive bool) error
}

// Ensurer is the verify-then-sign step every publisher runs after a push:
// a reference already carrying a valid signature for the resolved signing
// config is left alone, anything else is signed. Both seams are required.
type Ensurer struct {
	// Verify checks one signature subject (the cosign-backed verifier in
	// production; key-based in tests).
	Verify func(context.Context, verify.Subject) error
	// NewSigner builds a signer for a resolved signing config.
	NewSigner func(*spec.Signing) (ArtifactSigner, error)
}

// Valid reports whether ref (a digest reference) already carries a valid
// signature under signing.
func (e Ensurer) Valid(ctx context.Context, ref string, signing *spec.Signing) bool {
	if e.Verify == nil {
		return false
	}
	s, err := verify.MirrorSubject(ref, signing)
	if err != nil {
		return false
	}
	return e.Verify(ctx, s) == nil
}

// Ensure signs ref unless it already carries a valid signature under
// signing, reporting whether a signature was created. recursive also signs
// a manifest list's children.
func (e Ensurer) Ensure(ctx context.Context, ref string, signing *spec.Signing, recursive bool) (bool, error) {
	if e.Valid(ctx, ref, signing) {
		return false, nil
	}
	if err := e.Sign(ctx, ref, signing, recursive); err != nil {
		return false, err
	}
	return true, nil
}

// Sign signs ref under signing unconditionally — for callers that already
// ran Valid themselves (to narrate, or to stop short on a dry run).
func (e Ensurer) Sign(ctx context.Context, ref string, signing *spec.Signing, recursive bool) error {
	if e.NewSigner == nil {
		return errors.New("no signer configured")
	}
	signer, err := e.NewSigner(signing)
	if err != nil {
		return err
	}
	return signer.Sign(ctx, ref, recursive)
}
