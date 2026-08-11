// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package discover

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/bitwise-media-group/patchy/internal/mirror/imageref"
	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
)

// Input is one discovery run.
type Input struct {
	// Rendered is the rendered manifest stream.
	Rendered []byte
	// AppVersion expands {appVersion} in extra image references.
	AppVersion string
	// Extra are the manifest- and sidecar-declared images rendering
	// cannot see.
	Extra []spec.ExtraImage
	// Exclude drops discovered references matching these globs.
	Exclude []spec.ExcludePattern
	// AllowEmpty accepts a chart that renders no images.
	AllowEmpty bool
}

// Result is a discovery outcome.
type Result struct {
	// Images are the canonical source references, normalized, sorted and
	// deduplicated.
	Images []string
	// Excluded lists references dropped by exclude globs, for narration.
	Excluded []string
}

// Discover runs the four passes over the rendered stream.
func Discover(in Input) (Result, error) {
	refs, err := renderedRefs(in.Rendered)
	if err != nil {
		return Result{}, err
	}
	for _, e := range in.Extra {
		if e.Image != "" {
			refs = append(refs, strings.ReplaceAll(e.Image, "{appVersion}", in.AppVersion))
		}
	}

	seen := map[string]bool{}
	var out Result
	sort.Strings(refs)
	for _, ref := range refs {
		if ref == "" {
			continue
		}
		ref = imageref.Normalize(ref)
		if seen[ref] {
			continue
		}
		seen[ref] = true
		if pattern := excludedBy(ref, in.Exclude); pattern != "" {
			out.Excluded = append(out.Excluded, ref)
			continue
		}
		out.Images = append(out.Images, ref)
	}
	sort.Strings(out.Images)
	sort.Strings(out.Excluded)
	if len(out.Images) == 0 && !in.AllowEmpty {
		return Result{}, errors.New(
			"no images discovered; check discovery values (or set images.allowEmpty for CRD-only charts)",
		)
	}
	return out, nil
}

// excludedBy returns the first matching exclude glob, or "".
func excludedBy(ref string, excludes []spec.ExcludePattern) string {
	for _, e := range excludes {
		if e.Pattern != "" && imageref.GlobMatch(e.Pattern, ref) {
			return e.Pattern
		}
	}
	return ""
}

// renderedRefs runs the three rendered-output passes over every document.
func renderedRefs(rendered []byte) ([]string, error) {
	var refs []string
	dec := yaml.NewDecoder(strings.NewReader(string(rendered)))
	for {
		var doc any
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse rendered manifests: %w", err)
		}
		if doc == nil {
			continue
		}
		// Pass 1: pod-spec container lists at any depth.
		walkContainerImages(doc, &refs)
		if m, ok := doc.(map[string]any); ok {
			// Pass 2: operator-style CRs declare their workload image as
			// a top-level spec.image rather than a pod spec.
			if spc, ok := m["spec"].(map[string]any); ok {
				if img, ok := spc["image"].(string); ok && img != "" {
					refs = append(refs, img)
				}
			}
			// Pass 3: ConfigMap data sweep for image-shaped strings.
			if kind, _ := m["kind"].(string); kind == "ConfigMap" {
				if data, ok := m["data"].(map[string]any); ok {
					for _, v := range data {
						if s, ok := v.(string); ok {
							refs = append(refs, configMapImages(s)...)
						}
					}
				}
			}
		}
	}
	return refs, nil
}

// containerListKeys are the pod-spec keys whose sequence items carry images.
var containerListKeys = []string{"containers", "initContainers", "ephemeralContainers"}

// walkContainerImages recursively collects .image from container lists.
// Only sequence-valued keys count: CRDs embed pod-template *schemas* where
// these keys hold maps.
func walkContainerImages(node any, refs *[]string) {
	switch n := node.(type) {
	case map[string]any:
		for _, key := range containerListKeys {
			if seq, ok := n[key].([]any); ok {
				for _, item := range seq {
					if m, ok := item.(map[string]any); ok {
						if img, ok := m["image"].(string); ok && img != "" {
							*refs = append(*refs, img)
						}
					}
				}
			}
		}
		for _, v := range n {
			walkContainerImages(v, refs)
		}
	case []any:
		for _, v := range n {
			walkContainerImages(v, refs)
		}
	}
}

// configMapImageRE matches image-shaped assignments inside config text:
// `image: repo/path:tag`, `"image" = "repo/path:tag"`, requiring at least
// one path slash and a tag.
var configMapImageRE = regexp.MustCompile(
	`"?image"?[ \t]*[:=][ \t]*"?[a-z0-9][a-z0-9._/-]*(:[0-9]+)?/[a-z0-9._/-]+:[a-zA-Z0-9][a-zA-Z0-9._-]*"?`,
)

// configMapImageStripRE removes the assignment prefix from a match.
var configMapImageStripRE = regexp.MustCompile(`^"?image"?[ \t]*[:=][ \t]*"?`)

// configMapImages extracts image references from one ConfigMap data value.
// Matches carrying template syntax ({, }, $) are dropped — a templated ref
// is not a pullable pin.
func configMapImages(data string) []string {
	var out []string
	for line := range strings.SplitSeq(data, "\n") {
		for _, m := range configMapImageRE.FindAllString(line, -1) {
			ref := configMapImageStripRE.ReplaceAllString(m, "")
			ref = strings.TrimSuffix(ref, `"`)
			if strings.ContainsAny(ref, "{}$") {
				continue
			}
			out = append(out, ref)
		}
	}
	return out
}
