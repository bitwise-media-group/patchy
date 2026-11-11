// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package buildx

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/bitwise-media-group/patchy/internal/mirror/scan"
)

// Request is one image build.
type Request struct {
	// Dockerfile and Context are paths (absolute, or relative to the
	// process working directory).
	Dockerfile string
	Context    string
	// Platforms are os/arch[/variant] strings.
	Platforms []string
	// Target optionally names the build stage.
	Target string
	// Args are build args, emitted sorted by key.
	Args map[string]string
	// Secrets are BuildKit secret mounts, emitted sorted by ID. Each names
	// where docker reads the value (an environment variable or a file);
	// the value itself never enters the argv.
	Secrets []Secret
	// Output is the OCI layout directory the build exports to.
	Output string
	// SourceDateEpoch, when set, is passed to buildx and turns on
	// rewrite-timestamp for a reproducible digest.
	SourceDateEpoch string
}

// Secret is one BuildKit secret (RUN --mount=type=secret,id=<ID>): exactly
// one of Env (a variable in docker's environment) or Src (a file path) is
// set.
type Secret struct {
	ID  string
	Env string
	Src string
}

// spec renders the --secret value.
func (s Secret) spec() string {
	if s.Env != "" {
		return "id=" + s.ID + ",env=" + s.Env
	}
	return "id=" + s.ID + ",src=" + s.Src
}

// Args builds the docker argv for r (without the leading "docker").
func Args(r Request) []string {
	args := []string{"buildx", "build", "--file", r.Dockerfile, "--platform", strings.Join(r.Platforms, ",")}
	if r.Target != "" {
		args = append(args, "--target", r.Target)
	}
	for _, k := range slices.Sorted(maps.Keys(r.Args)) {
		args = append(args, "--build-arg", k+"="+r.Args[k])
	}
	byID := func(a, b Secret) int { return strings.Compare(a.ID, b.ID) }
	for _, s := range slices.SortedFunc(slices.Values(r.Secrets), byID) {
		args = append(args, "--secret", s.spec())
	}
	output := "type=oci,dest=" + r.Output + ",tar=false"
	if r.SourceDateEpoch != "" {
		output += ",rewrite-timestamp=true"
	}
	return append(args, "--output", output, r.Context)
}

// Builder runs builds through a tool runner.
type Builder struct {
	// Runner executes docker (nil: the real PATH).
	Runner scan.ToolRunner
}

// Build runs one build, exporting the OCI layout to r.Output.
func (b Builder) Build(ctx context.Context, r Request) error {
	runner := b.Runner
	if runner == nil {
		runner = scan.ExecRunner{}
	}
	if !runner.Look("docker") {
		return errors.New("docker is not on PATH: patchy build needs docker with the buildx plugin")
	}
	var env []string
	if r.SourceDateEpoch != "" {
		env = append(env, "SOURCE_DATE_EPOCH="+r.SourceDateEpoch)
	}
	if _, err := runner.Run(ctx, "docker", Args(r), env); err != nil {
		return classify(err)
	}
	return nil
}

// Failure signatures worth a pointed hint rather than a raw build log.
var (
	missingBuildx = []string{
		"'buildx' is not a docker command",
		"unknown command: docker buildx",
		`unknown command "buildx"`,
	}
	unsupportedExport = []string{
		"Multi-platform build is not supported for the docker driver",
		"multiple platforms feature is currently not supported for docker driver",
		"OCI exporter is not supported for the docker driver",
		"docker exporter does not currently support exporting manifest lists",
	}
)

// classify maps a failed build onto an actionable error when the failure
// is the toolchain rather than the Dockerfile.
func classify(err error) error {
	msg := err.Error()
	for _, s := range missingBuildx {
		if strings.Contains(msg, s) {
			return fmt.Errorf("docker buildx is not installed (install the buildx plugin): %w", err)
		}
	}
	for _, s := range unsupportedExport {
		if strings.Contains(msg, s) {
			return fmt.Errorf("the active buildx builder cannot export a multi-platform OCI layout: "+
				"use a docker-container builder (docker buildx create --use --driver docker-container) "+
				"or enable the containerd image store: %w", err)
		}
	}
	return fmt.Errorf("docker buildx build: %w", err)
}
