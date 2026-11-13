// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package build

import (
	"fmt"
	"os"
	"strings"

	"github.com/bitwise-media-group/patchy/internal/build/dockerfile"
	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
)

// stagePolicy is the event stage source-policy narration uses.
const stagePolicy = "policy"

// enforcePolicy holds every image the entry's Dockerfile pulls (FROM,
// COPY --from, RUN --mount from=, the frontend) against the config's
// source policy before buildx runs: warn-mode violations narrate, blocking
// ones fail the build with nothing pulled. A reference that cannot be
// resolved (an unset build arg) cannot be proven permitted, so it is
// rejected under the strictest layer restricting images. A config with no
// image policy skips the check (and the Dockerfile read) entirely.
func (e *Engine) enforcePolicy(entry Entry, args map[string]string) error {
	policy := e.global.Policy
	if !policy.Restricts(spec.SourceImage) {
		return nil
	}
	path := e.path(entry.Image.DockerfilePath())
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("source policy: read %s: %w", e.relPath(path), err)
	}
	sources, err := dockerfile.Sources(dockerfile.Input{
		Content: content, Args: args, Platforms: entry.Platforms,
	})
	if err != nil {
		return fmt.Errorf("source policy: %s: %w", e.relPath(path), err)
	}
	var blocking []string
	for _, s := range sources {
		var v *spec.PolicyViolation
		if s.Err != "" {
			v = policy.Reject(spec.SourceImage, s.Raw, "cannot be resolved: "+s.Err)
		} else {
			v = policy.Check(spec.SourceImage, s.Ref)
		}
		if v == nil {
			continue
		}
		msg := fmt.Sprintf("%s: %s", e.where(path, s), v)
		if v.Blocking() {
			blocking = append(blocking, msg)
			continue
		}
		e.warnf(entry.Name, stagePolicy, "%s", msg)
	}
	if len(blocking) > 0 {
		return fmt.Errorf("%s; nothing built", strings.Join(blocking, "; "))
	}
	return nil
}

// where locates a source for messages: Dockerfile path, line, instruction.
func (e *Engine) where(path string, s dockerfile.Source) string {
	if s.Line == 0 {
		return fmt.Sprintf("%s (build arg BUILDKIT_SYNTAX)", e.relPath(path))
	}
	return fmt.Sprintf("%s:%d (%s)", e.relPath(path), s.Line, s.Instruction)
}
