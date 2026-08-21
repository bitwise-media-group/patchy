// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package verify

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
)

func TestUpstreamSubject(t *testing.T) {
	tests := []struct {
		name    string
		rule    spec.VerifyRule
		want    Subject
		wantErr string
	}{
		{
			name:    "keyless requires an identity regexp",
			rule:    spec.VerifyRule{Provider: "cosign-keyless"},
			wantErr: "certificateIdentityRegexp",
		},
		{
			name: "keyless defaults the issuer",
			rule: spec.VerifyRule{Provider: "cosign-keyless", CertificateIdentityRegexp: "^https://github.com/acme/"},
			want: Subject{IdentityRegexp: "^https://github.com/acme/", Issuer: "https://token.actions.githubusercontent.com"},
		},
		{
			name: "keyless keeps an explicit issuer",
			rule: spec.VerifyRule{
				Provider:                  "cosign-keyless",
				CertificateIdentityRegexp: ".*",
				CertificateOidcIssuer:     "https://issuer.example.test",
			},
			want: Subject{IdentityRegexp: ".*", Issuer: "https://issuer.example.test"},
		},
		{
			name:    "key requires a key",
			rule:    spec.VerifyRule{Provider: "cosign-key"},
			wantErr: "requires key",
		},
		{
			name: "key defaults to sha256 and skips the tlog",
			rule: spec.VerifyRule{Provider: "cosign-key", Key: "cosign.pub"},
			want: Subject{KeyRef: "cosign.pub", IgnoreTlog: true, HashAlgorithm: "sha256"},
		},
		{
			name: "key maps sha384",
			rule: spec.VerifyRule{Provider: "cosign-key", Key: "cosign.pub", SignatureDigestAlgorithm: "sha384"},
			want: Subject{KeyRef: "cosign.pub", IgnoreTlog: true, HashAlgorithm: "sha384"},
		},
		{
			name: "key maps sha512",
			rule: spec.VerifyRule{Provider: "cosign-key", Key: "cosign.pub", SignatureDigestAlgorithm: "sha512"},
			want: Subject{KeyRef: "cosign.pub", IgnoreTlog: true, HashAlgorithm: "sha512"},
		},
		{
			name:    "key rejects unknown digest algorithms",
			rule:    spec.VerifyRule{Provider: "cosign-key", Key: "cosign.pub", SignatureDigestAlgorithm: "md5"},
			wantErr: "unsupported signatureDigestAlgorithm",
		},
		{
			name: "signature repository propagates",
			rule: spec.VerifyRule{
				Provider: "cosign-key", Key: "cosign.pub",
				SignatureRepository: "ghcr.example.test/signatures",
			},
			want: Subject{
				KeyRef: "cosign.pub", IgnoreTlog: true, HashAlgorithm: "sha256",
				SignatureRepository: "ghcr.example.test/signatures",
			},
		},
		{
			name:    "none is the caller's business, never a subject",
			rule:    spec.VerifyRule{},
			wantErr: "unsupported provider",
		},
		{
			name:    "unknown provider",
			rule:    spec.VerifyRule{Provider: "gpg"},
			wantErr: "unsupported provider",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := UpstreamSubject("reg.example.test/app:1.0.0", tt.rule)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("UpstreamSubject error = %v, want one containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("UpstreamSubject: %v", err)
			}
			tt.want.Ref = "reg.example.test/app:1.0.0"
			if got != tt.want {
				t.Errorf("UpstreamSubject = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestMirrorSubject(t *testing.T) {
	tests := []struct {
		name    string
		signing *spec.Signing
		want    Subject
		wantErr string
	}{
		{
			name:    "keyless requires the keyless block",
			signing: &spec.Signing{Provider: "keyless"},
			wantErr: "lacks certificateIdentity",
		},
		{
			name:    "keyless requires the identity",
			signing: &spec.Signing{Provider: "keyless", Keyless: &spec.KeylessSigning{}},
			wantErr: "lacks certificateIdentity",
		},
		{
			name: "keyless defaults the issuer",
			signing: &spec.Signing{Provider: "keyless", Keyless: &spec.KeylessSigning{
				CertificateIdentity: "https://github.com/acme/mirror/.github/workflows/publish.yaml@refs/heads/main",
			}},
			want: Subject{
				Identity: "https://github.com/acme/mirror/.github/workflows/publish.yaml@refs/heads/main",
				Issuer:   "https://token.actions.githubusercontent.com",
			},
		},
		{
			name: "keyless keeps an explicit issuer",
			signing: &spec.Signing{Provider: "keyless", Keyless: &spec.KeylessSigning{
				CertificateIdentity:   "https://ci.example.test/publish",
				CertificateOidcIssuer: "https://issuer.example.test",
			}},
			want: Subject{Identity: "https://ci.example.test/publish", Issuer: "https://issuer.example.test"},
		},
		{
			name:    "kms requires the kms block",
			signing: &spec.Signing{Provider: "kms"},
			wantErr: "lacks key",
		},
		{
			name:    "kms requires a key",
			signing: &spec.Signing{Provider: "kms", KMS: &spec.KMSSigning{}},
			wantErr: "lacks key",
		},
		{
			name: "kms verifies against the key offline",
			signing: &spec.Signing{Provider: "kms", KMS: &spec.KMSSigning{
				Key: "gcpkms://projects/p/locations/l/keyRings/r/cryptoKeys/k",
			}},
			want: Subject{
				KeyRef:        "gcpkms://projects/p/locations/l/keyRings/r/cryptoKeys/k",
				HashAlgorithm: "sha256",
				IgnoreTlog:    true,
			},
		},
		{
			name:    "unknown provider",
			signing: &spec.Signing{Provider: "pgp"},
			wantErr: "unsupported signing provider",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := MirrorSubject("reg.example.test/app@sha256:abc", tt.signing)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("MirrorSubject error = %v, want one containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("MirrorSubject: %v", err)
			}
			tt.want.Ref = "reg.example.test/app@sha256:abc"
			if got != tt.want {
				t.Errorf("MirrorSubject = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// cannedRunner records the cosign invocation and returns fixed results.
type cannedRunner struct {
	err  error
	have bool
	runs int
	args []string
	envs []string
}

func (r *cannedRunner) Run(_ context.Context, _ string, args, env []string) ([]byte, error) {
	r.runs++
	r.args = args
	r.envs = env
	return nil, r.err
}

func (r *cannedRunner) Look(string) bool { return r.have }

// TestVerifyRejectsUnconstrainedIdentity pins the defense-in-depth guard:
// cosign treats an identity with neither subject nor regexp as matching
// any signer, so Verify must refuse the shape outright — before any exec —
// rather than "verifying" against the whole world.
func TestVerifyRejectsUnconstrainedIdentity(t *testing.T) {
	runner := &cannedRunner{have: true}
	err := Verify(context.Background(), runner, Subject{Ref: "reg.example.test/app:1.0.0"})
	if err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("Verify error = %v, want the unconstrained-identity refusal", err)
	}
	if runner.runs != 0 {
		t.Fatalf("Verify ran cosign %d times before the guard", runner.runs)
	}
}

func TestVerifyMissingBinary(t *testing.T) {
	runner := &cannedRunner{have: false}
	err := Verify(context.Background(), runner, Subject{Ref: "reg.example.test/app:1.0.0", KeyRef: "cosign.pub"})
	if err == nil || !strings.Contains(err.Error(), "cosign not found on PATH") {
		t.Fatalf("Verify error = %v, want the missing-binary hint", err)
	}
	if runner.runs != 0 {
		t.Fatalf("Verify ran cosign %d times without the binary", runner.runs)
	}
}

func TestVerifyArgs(t *testing.T) {
	tests := []struct {
		name     string
		subject  Subject
		wantArgs []string
		wantEnvs []string
	}{
		{
			name:    "key with defaulted digest",
			subject: Subject{Ref: "reg.example.test/app:1.0.0", KeyRef: "cosign.pub"},
			wantArgs: []string{
				"verify", "--key", "cosign.pub", "--signature-digest-algorithm=sha256",
				"reg.example.test/app:1.0.0",
			},
		},
		{
			name: "key offline against a plain-http registry",
			subject: Subject{
				Ref: "reg.example.test/app@sha256:abc", KeyRef: "cosign.pub",
				HashAlgorithm: "sha512", IgnoreTlog: true, AllowHTTPRegistry: true,
			},
			wantArgs: []string{
				"verify", "--key", "cosign.pub", "--signature-digest-algorithm=sha512",
				"--insecure-ignore-tlog=true", "--allow-insecure-registry",
				"reg.example.test/app@sha256:abc",
			},
		},
		{
			name: "keyless with an exact identity",
			subject: Subject{
				Ref: "reg.example.test/app:1.0.0", Identity: "https://ci.example.test/publish",
				Issuer: "https://issuer.example.test",
			},
			wantArgs: []string{
				"verify", "--certificate-identity=https://ci.example.test/publish",
				"--certificate-oidc-issuer=https://issuer.example.test",
				"reg.example.test/app:1.0.0",
			},
		},
		{
			name: "keyless with an identity regexp",
			subject: Subject{
				Ref: "reg.example.test/app:1.0.0", IdentityRegexp: "^https://github.com/acme/",
				Issuer: "https://token.actions.githubusercontent.com",
			},
			wantArgs: []string{
				"verify", "--certificate-identity-regexp=^https://github.com/acme/",
				"--certificate-oidc-issuer=https://token.actions.githubusercontent.com",
				"reg.example.test/app:1.0.0",
			},
		},
		{
			name: "signature repository via env",
			subject: Subject{
				Ref: "reg.example.test/app:1.0.0", KeyRef: "cosign.pub",
				SignatureRepository: "ghcr.example.test/signatures",
			},
			wantArgs: []string{
				"verify", "--key", "cosign.pub", "--signature-digest-algorithm=sha256",
				"reg.example.test/app:1.0.0",
			},
			wantEnvs: []string{"COSIGN_REPOSITORY=ghcr.example.test/signatures"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &cannedRunner{have: true}
			if err := Verify(context.Background(), runner, tt.subject); err != nil {
				t.Fatalf("Verify: %v", err)
			}
			if !slices.Equal(runner.args, tt.wantArgs) {
				t.Errorf("args = %q, want %q", runner.args, tt.wantArgs)
			}
			if !slices.Equal(runner.envs, tt.wantEnvs) {
				t.Errorf("envs = %q, want %q", runner.envs, tt.wantEnvs)
			}
		})
	}
}

func TestVerifyWrapsRunError(t *testing.T) {
	sentinel := errors.New("no matching signatures")
	runner := &cannedRunner{have: true, err: sentinel}
	err := Verify(context.Background(), runner, Subject{Ref: "reg.example.test/app:1.0.0", KeyRef: "cosign.pub"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Verify error = %v, want it to wrap the run error", err)
	}
	if !strings.Contains(err.Error(), "verify reg.example.test/app:1.0.0") {
		t.Fatalf("Verify error = %v, want the ref in the message", err)
	}
}
