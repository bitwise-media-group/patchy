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

// PathMatch reports whether value matches a path-aware glob pattern: '*'
// matches any run of characters within one '/'-separated segment, '**'
// matches any run including '/', and '?' matches exactly one character
// other than '/'. A "**/" that begins a segment also matches zero
// segments, so "ghcr.io/**/app" matches both ghcr.io/app and
// ghcr.io/a/b/app. No other metacharacters are supported.
//
// It is the one glob dialect of every reference pattern list — images.exclude,
// images.verifyUpstream match, ociRefs.allow and the source policies — so a
// segment boundary always holds ("ghcr.io/acme/*" never reaches into
// ghcr.io/acme/team/app).
func PathMatch(pattern, value string) bool {
	return pathMatch(pattern, value, true)
}

// pathMatch is PathMatch's body; segStart reports whether pattern begins at
// a segment boundary (the start, or just after a '/').
func pathMatch(pattern, value string, segStart bool) bool {
	for {
		if pattern == "" {
			return value == ""
		}
		switch {
		case strings.HasPrefix(pattern, "**"):
			rest := strings.TrimLeft(pattern, "*")
			if segStart && strings.HasPrefix(rest, "/") && pathMatch(rest[1:], value, true) {
				return true
			}
			for i := 0; i <= len(value); i++ {
				if pathMatch(rest, value[i:], i > 0 && value[i-1] == '/') {
					return true
				}
			}
			return false
		case pattern[0] == '*':
			rest := pattern[1:]
			for i := 0; i <= len(value); i++ {
				if pathMatch(rest, value[i:], false) {
					return true
				}
				if i < len(value) && value[i] == '/' {
					return false
				}
			}
			return false
		case pattern[0] == '?':
			if value == "" || value[0] == '/' {
				return false
			}
			pattern, value, segStart = pattern[1:], value[1:], false
		default:
			if value == "" || pattern[0] != value[0] {
				return false
			}
			segStart = pattern[0] == '/'
			pattern, value = pattern[1:], value[1:]
		}
	}
}
