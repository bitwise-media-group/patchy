// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package buildx

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// cannedRunner records one invocation and returns a canned result.
type cannedRunner struct {
	have bool
	err  error
	name string
	args []string
	env  []string
}

func (r *cannedRunner) Run(_ context.Context, name string, args, env []string) ([]byte, error) {
	r.name, r.args, r.env = name, args, env
	return nil, r.err
}

func (r *cannedRunner) Look(string) bool { return r.have }

func TestArgs(t *testing.T) {
	tests := []struct {
		name string
		r    Request
		want string
	}{
		{
			name: "minimal",
			r: Request{Dockerfile: "Dockerfile", Context: ".", Platforms: []string{"linux/amd64"},
				Output: "/tmp/oci"},
			want: "buildx build --file Dockerfile --platform linux/amd64 " +
				"--output type=oci,dest=/tmp/oci,tar=false .",
		},
		{
			name: "everything, args sorted",
			r: Request{
				Dockerfile: "build/Dockerfile.controller", Context: "src",
				Platforms: []string{"linux/amd64", "linux/arm64/v8"}, Target: "runtime",
				Args:   map[string]string{"GO_VERSION": "1.27", "CGO_ENABLED": "0"},
				Output: "/tmp/oci", SourceDateEpoch: "1700000000",
			},
			want: "buildx build --file build/Dockerfile.controller --platform linux/amd64,linux/arm64/v8 " +
				"--target runtime --build-arg CGO_ENABLED=0 --build-arg GO_VERSION=1.27 " +
				"--output type=oci,dest=/tmp/oci,tar=false,rewrite-timestamp=true src",
		},
	}
	for _, tt := range tests {
		if got := strings.Join(Args(tt.r), " "); got != tt.want {
			t.Errorf("%s:\n got %s\nwant %s", tt.name, got, tt.want)
		}
	}
}

func TestBuild(t *testing.T) {
	r := Request{Dockerfile: "Dockerfile", Context: ".", Platforms: []string{"linux/amd64"},
		Output: "/tmp/oci", SourceDateEpoch: "1700000000"}

	runner := &cannedRunner{have: true}
	if err := (Builder{Runner: runner}).Build(context.Background(), r); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if runner.name != "docker" || runner.args[0] != "buildx" ||
		strings.Join(runner.env, " ") != "SOURCE_DATE_EPOCH=1700000000" {
		t.Errorf("ran %s %v env %v", runner.name, runner.args, runner.env)
	}

	tests := []struct {
		name    string
		runner  *cannedRunner
		wantErr string
	}{
		{"no docker", &cannedRunner{have: false}, "docker is not on PATH"},
		{
			"no buildx",
			&cannedRunner{have: true, err: errors.New("docker: exit status 1 (docker: 'buildx' is not a docker command.)")},
			"buildx is not installed",
		},
		{
			"docker driver",
			&cannedRunner{have: true, err: errors.New("docker: exit status 1 (ERROR: Multi-platform build is " +
				"not supported for the docker driver.)")},
			"docker-container",
		},
		{"dockerfile failure", &cannedRunner{have: true, err: errors.New("exit status 1 (RUN false)")}, "RUN false"},
	}
	for _, tt := range tests {
		err := (Builder{Runner: tt.runner}).Build(context.Background(), r)
		if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
			t.Errorf("%s: Build = %v, want %q", tt.name, err, tt.wantErr)
		}
	}
}
