// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package imageref

import (
	"fmt"
	"strings"
)

// Ref is a container image reference split into its addressable parts.
// Repository always carries an explicit registry host (Normalize first when
// the input may be shorthand).
type Ref struct {
	// Repository is the registry host plus path, e.g. "ghcr.io/acme/app".
	Repository string
	// Tag is the tag portion, "latest" when the reference carries none.
	Tag string
	// Digest is the "sha256:..." pin when present, empty otherwise.
	Digest string
}

// String reassembles the reference: repository, ":tag" when set, "@digest"
// when set.
func (r Ref) String() string {
	s := r.Repository
	if r.Tag != "" {
		s += ":" + r.Tag
	}
	if r.Digest != "" {
		s += "@" + r.Digest
	}
	return s
}

// Normalize expands shorthand image references the way container runtimes
// do: a bare name gains docker.io/library/, a path without a registry host
// gains docker.io/, and a single-segment docker.io path gains library/.
func Normalize(ref string) string {
	if !strings.Contains(ref, "/") {
		return "docker.io/library/" + ref
	}
	first := ref[:strings.Index(ref, "/")]
	// A first segment with no dot or port is a path component, not a host.
	if !strings.Contains(first, ".") && !strings.Contains(first, ":") && first != "localhost" {
		ref = "docker.io/" + ref
	}
	if path, ok := strings.CutPrefix(ref, "docker.io/"); ok && !strings.Contains(path, "/") {
		ref = "docker.io/library/" + path
	}
	return ref
}

// Parse splits a reference into repository, tag and digest. It tolerates
// pinned digests (repo:tag@sha256:... and repo@sha256:...) and registry
// ports (localhost:5000/app:v1). A reference with no tag parses with
// Tag "latest", matching runtime behaviour.
func Parse(ref string) (Ref, error) {
	if ref == "" {
		return Ref{}, fmt.Errorf("empty image reference")
	}
	r := Ref{Tag: "latest"}
	base := ref
	if i := strings.Index(base, "@"); i >= 0 {
		r.Digest = base[i+1:]
		base = base[:i]
		if r.Digest == "" {
			return Ref{}, fmt.Errorf("image reference %q has an empty digest", ref)
		}
		// A digest-only pin carries no tag.
		r.Tag = ""
	}
	// The tag separator is a colon after the last slash; a colon before it
	// is a registry port.
	last := base[strings.LastIndex(base, "/")+1:]
	if i := strings.LastIndex(last, ":"); i >= 0 {
		r.Tag = last[i+1:]
		base = base[:len(base)-len(last)+i]
		if r.Tag == "" {
			return Ref{}, fmt.Errorf("image reference %q has an empty tag", ref)
		}
	}
	if base == "" {
		return Ref{}, fmt.Errorf("image reference %q has no repository", ref)
	}
	r.Repository = base
	return r, nil
}

// Rewrite routes a pull through a per-host rewrite (e.g. a pull-through
// cache in front of docker.io) when one is configured for the reference's
// registry host. References whose host has no rewrite pass through
// unchanged; recorded sources and publish targets always keep the canonical
// name.
func Rewrite(ref string, rewrites map[string]string) string {
	host, rest, ok := strings.Cut(ref, "/")
	if !ok {
		return ref
	}
	if target := rewrites[host]; target != "" {
		return target + "/" + rest
	}
	return ref
}

// GlobMatch reports whether value matches a shell-style glob pattern as a
// bash case statement would: '*' matches any run of characters including
// '/', '?' matches exactly one. No other metacharacters are supported.
func GlobMatch(pattern, value string) bool {
	return globMatch(pattern, value)
}

func globMatch(pattern, value string) bool {
	for {
		if pattern == "" {
			return value == ""
		}
		switch pattern[0] {
		case '*':
			// Collapse consecutive stars, then try every split point.
			pattern = strings.TrimLeft(pattern, "*")
			if pattern == "" {
				return true
			}
			for i := 0; i <= len(value); i++ {
				if globMatch(pattern, value[i:]) {
					return true
				}
			}
			return false
		case '?':
			if value == "" {
				return false
			}
			pattern, value = pattern[1:], value[1:]
		default:
			if value == "" || pattern[0] != value[0] {
				return false
			}
			pattern, value = pattern[1:], value[1:]
		}
	}
}
