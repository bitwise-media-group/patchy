// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package sign

import (
	"context"
	"errors"
	"testing"

	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
	"github.com/bitwise-media-group/patchy/internal/mirror/verify"
)

// recordingSigner records every Sign call.
type recordingSigner struct {
	refs      []string
	recursive []bool
	err       error
}

func (r *recordingSigner) Sign(_ context.Context, ref string, recursive bool) error {
	r.refs = append(r.refs, ref)
	r.recursive = append(r.recursive, recursive)
	return r.err
}

func TestEnsurer(t *testing.T) {
	keyless := &spec.Signing{Provider: "keyless", Keyless: &spec.KeylessSigning{CertificateIdentity: "id"}}
	kms := &spec.Signing{Provider: "kms", KMS: &spec.KMSSigning{Key: "awskms://alias/k"}}
	const ref = "registry.example.com/app@sha256:abc"

	t.Run("already signed is left alone", func(t *testing.T) {
		signer := &recordingSigner{}
		var subjects []verify.Subject
		e := Ensurer{
			Verify: func(_ context.Context, s verify.Subject) error {
				subjects = append(subjects, s)
				return nil
			},
			NewSigner: func(*spec.Signing) (ArtifactSigner, error) { return signer, nil },
		}
		signed, err := e.Ensure(context.Background(), ref, keyless, true)
		if err != nil || signed || len(signer.refs) != 0 {
			t.Errorf("Ensure = %v, %v; signer calls %v", signed, err, signer.refs)
		}
		if len(subjects) != 1 || subjects[0].Identity != "id" || subjects[0].Ref != ref {
			t.Errorf("verified subjects = %+v", subjects)
		}
	})

	t.Run("unsigned is signed with the resolved config", func(t *testing.T) {
		signer := &recordingSigner{}
		var built []*spec.Signing
		e := Ensurer{
			Verify: func(context.Context, verify.Subject) error { return errors.New("no signature") },
			NewSigner: func(s *spec.Signing) (ArtifactSigner, error) {
				built = append(built, s)
				return signer, nil
			},
		}
		signed, err := e.Ensure(context.Background(), ref, kms, false)
		if err != nil || !signed {
			t.Fatalf("Ensure = %v, %v", signed, err)
		}
		if len(built) != 1 || built[0] != kms || len(signer.refs) != 1 || signer.recursive[0] {
			t.Errorf("signer built %+v, calls %v recursive %v", built, signer.refs, signer.recursive)
		}
	})

	t.Run("signing failures propagate", func(t *testing.T) {
		e := Ensurer{
			Verify:    func(context.Context, verify.Subject) error { return errors.New("no signature") },
			NewSigner: func(*spec.Signing) (ArtifactSigner, error) { return &recordingSigner{err: errors.New("boom")}, nil },
		}
		if signed, err := e.Ensure(context.Background(), ref, kms, false); err == nil || signed {
			t.Errorf("Ensure = %v, %v; want the signer's error", signed, err)
		}
	})

	t.Run("no signer configured", func(t *testing.T) {
		if _, err := (Ensurer{}).Ensure(context.Background(), ref, kms, false); err == nil {
			t.Error("Ensure without seams succeeded")
		}
	})
}
