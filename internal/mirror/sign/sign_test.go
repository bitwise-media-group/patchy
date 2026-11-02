// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package sign

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
)

// cannedRunner records the cosign invocation and returns fixed results.
// The signing config file only lives for the run, so it is read then.
type cannedRunner struct {
	err        error
	have       bool
	runs       int
	args       []string
	envs       []string
	configPath string
	config     string
}

func (r *cannedRunner) Run(_ context.Context, _ string, args, env []string) ([]byte, error) {
	r.runs++
	r.args = args
	r.envs = env
	for _, a := range args {
		if path, ok := strings.CutPrefix(a, "--signing-config="); ok {
			b, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			r.configPath, r.config = path, string(b)
		}
	}
	return nil, r.err
}

func (r *cannedRunner) Look(string) bool { return r.have }

// The signing configs each mode renders: the pinned public services for
// keyless, none at all for a key.
const (
	fulcioJSON = `{"url":"https://fulcio.sigstore.dev","majorApiVersion":1,` +
		`"validFor":{"start":"2022-04-13T20:06:15Z"},"operator":"sigstore.dev"}`
	oidcJSON = `{"url":"https://oauth2.sigstore.dev/auth","majorApiVersion":1,` +
		`"validFor":{"start":"2022-04-13T20:06:15Z"},"operator":"sigstore.dev"}`
	rekorJSON = `{"url":"https://rekor.sigstore.dev","majorApiVersion":1,` +
		`"validFor":{"start":"2021-01-12T11:53:27Z"},"operator":"sigstore.dev"}`

	keyConfig = `{"mediaType":"application/vnd.dev.sigstore.signingconfig.v0.2+json",` +
		`"rekorTlogConfig":{},"tsaConfig":{}}`
	keylessConfig = `{"mediaType":"application/vnd.dev.sigstore.signingconfig.v0.2+json",` +
		`"caUrls":[` + fulcioJSON + `],"oidcUrls":[` + oidcJSON + `],` +
		`"rekorTlogUrls":[` + rekorJSON + `],"rekorTlogConfig":{"selector":"ANY"},"tsaConfig":{}}`
	keylessNoTlogConfig = `{"mediaType":"application/vnd.dev.sigstore.signingconfig.v0.2+json",` +
		`"caUrls":[` + fulcioJSON + `],"oidcUrls":[` + oidcJSON + `],` +
		`"rekorTlogConfig":{},"tsaConfig":{}}`
)

func TestNew(t *testing.T) {
	tests := []struct {
		name    string
		signing *spec.Signing
		wantErr string
	}{
		{name: "keyless", signing: &spec.Signing{Provider: "keyless"}},
		{name: "kms", signing: &spec.Signing{Provider: "kms", KMS: &spec.KMSSigning{Key: "gcpkms://k"}}},
		{name: "kms requires the block", signing: &spec.Signing{Provider: "kms"}, wantErr: "lacks key"},
		{name: "kms requires a key", signing: &spec.Signing{Provider: "kms", KMS: &spec.KMSSigning{}}, wantErr: "lacks key"},
		{name: "unknown provider", signing: &spec.Signing{Provider: "pgp"}, wantErr: "unsupported signing provider"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(tt.signing, &cannedRunner{have: true})
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("New: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("New error = %v, want one containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestSignMissingBinary(t *testing.T) {
	runner := &cannedRunner{have: false}
	s, err := New(&spec.Signing{Provider: "kms", KMS: &spec.KMSSigning{Key: "gcpkms://k"}}, runner)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = s.Sign(context.Background(), "reg.example.test/app@sha256:abc", false)
	if err == nil || !strings.Contains(err.Error(), "cosign not found on PATH") {
		t.Fatalf("Sign error = %v, want the missing-binary hint", err)
	}
	if runner.runs != 0 {
		t.Fatalf("Sign ran cosign %d times without the binary", runner.runs)
	}
}

func TestSignArgs(t *testing.T) {
	tlogOff := false
	tests := []struct {
		name       string
		signer     func(r *cannedRunner) (*Signer, error)
		wantArgs   []string
		wantEnvs   []string
		wantConfig string
	}{
		{
			name: "kms",
			signer: func(r *cannedRunner) (*Signer, error) {
				return New(&spec.Signing{Provider: "kms", KMS: &spec.KMSSigning{Key: "awskms://alias/mirror"}}, r)
			},
			wantArgs: []string{
				"sign", "--yes", "--recursive=false", "--signing-config=*",
				"--key", "awskms://alias/mirror",
				"reg.example.test/app@sha256:abc",
			},
			wantConfig: keyConfig,
		},
		{
			name: "keyless with the tlog",
			signer: func(r *cannedRunner) (*Signer, error) {
				return New(&spec.Signing{Provider: "keyless", Keyless: &spec.KeylessSigning{}}, r)
			},
			wantArgs: []string{
				"sign", "--yes", "--recursive=false", "--signing-config=*",
				"reg.example.test/app@sha256:abc",
			},
			wantConfig: keylessConfig,
		},
		{
			name: "keyless with the tlog disabled",
			signer: func(r *cannedRunner) (*Signer, error) {
				return New(&spec.Signing{Provider: "keyless", Keyless: &spec.KeylessSigning{TlogUpload: &tlogOff}}, r)
			},
			wantArgs: []string{
				"sign", "--yes", "--recursive=false", "--signing-config=*",
				"reg.example.test/app@sha256:abc",
			},
			wantConfig: keylessNoTlogConfig,
		},
		{
			name: "key file offline",
			signer: func(r *cannedRunner) (*Signer, error) {
				return NewWithKeyFile("cosign.key", []byte("hunter2"), r), nil
			},
			wantArgs: []string{
				"sign", "--yes", "--recursive=false", "--signing-config=*",
				"--key", "cosign.key", "--allow-insecure-registry",
				"reg.example.test/app@sha256:abc",
			},
			wantEnvs:   []string{"COSIGN_PASSWORD=hunter2"},
			wantConfig: keyConfig,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &cannedRunner{have: true}
			s, err := tt.signer(runner)
			if err != nil {
				t.Fatalf("build signer: %v", err)
			}
			if err := s.Sign(context.Background(), "reg.example.test/app@sha256:abc", false); err != nil {
				t.Fatalf("Sign: %v", err)
			}
			// The config path is a fresh temp file per run.
			args := slices.Clone(runner.args)
			for i, a := range args {
				if strings.HasPrefix(a, "--signing-config=") {
					args[i] = "--signing-config=*"
				}
			}
			if !slices.Equal(args, tt.wantArgs) {
				t.Errorf("args = %q, want %q", runner.args, tt.wantArgs)
			}
			if !slices.Equal(runner.envs, tt.wantEnvs) {
				t.Errorf("envs = %q, want %q", runner.envs, tt.wantEnvs)
			}
			if runner.config != tt.wantConfig {
				t.Errorf("signing config = %s, want %s", runner.config, tt.wantConfig)
			}
			if _, err := os.Stat(runner.configPath); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("signing config %s outlived the run (stat: %v)", runner.configPath, err)
			}
		})
	}
}

func TestSignRecursive(t *testing.T) {
	runner := &cannedRunner{have: true}
	s, err := New(&spec.Signing{Provider: "kms", KMS: &spec.KMSSigning{Key: "gcpkms://k"}}, runner)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.Sign(context.Background(), "reg.example.test/app@sha256:abc", true); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if !slices.Contains(runner.args, "--recursive=true") {
		t.Errorf("args = %q, want --recursive=true", runner.args)
	}
}

func TestSignWrapsRunError(t *testing.T) {
	sentinel := errors.New("registry unavailable")
	runner := &cannedRunner{have: true, err: sentinel}
	s := NewWithKeyFile("cosign.key", nil, runner)
	err := s.Sign(context.Background(), "reg.example.test/app@sha256:abc", false)
	if !errors.Is(err, sentinel) {
		t.Fatalf("Sign error = %v, want it to wrap the run error", err)
	}
	if !strings.Contains(err.Error(), "sign reg.example.test/app@sha256:abc") {
		t.Fatalf("Sign error = %v, want the ref in the message", err)
	}
}
