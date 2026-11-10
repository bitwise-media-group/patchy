// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package build

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/google/go-containerregistry/pkg/name"

	"github.com/bitwise-media-group/patchy/internal/build/fluxartifact"
)

const (
	// ActionPromoted means the target tag was absent and now points at the
	// promoted build.
	ActionPromoted = "promoted"
	// ActionWouldPromote is the promote dry-run row: nothing was tagged.
	ActionWouldPromote = "would-promote"
)

// PromoteOptions tunes one Promote call.
type PromoteOptions struct {
	// From is a glob over the repository's tags (path.Match syntax, after
	// ${ENV} expansion) selecting the builds a promotion may start from,
	// e.g. "${VERSION}-edge.*". The newest match, in semver order, wins.
	From string
	// Tag is the tag to add to that build (after ${ENV} expansion).
	Tag string
	// Registries restricts promotion to the named registries (nil: all).
	Registries []string
	// DryRun resolves and verifies the build to promote, but tags nothing.
	DryRun bool
}

// PromoteRecord is one registry's promotion of one entry.
type PromoteRecord struct {
	// Registry is the configured registry name.
	Registry string `json:"registry"`
	// Source is the build promoted, <registry.url>/<repository>:<tag>.
	Source string `json:"source"`
	// Ref is the promoted tag, <registry.url>/<repository>:<tag>.
	Ref string `json:"ref"`
	// Digest is the build's manifest (or index) digest.
	Digest string `json:"digest"`
	// Revision is the build's org.opencontainers.image.revision annotation,
	// when it carries one (every manifests artifact does): what the build
	// was made from, for a caller to check against what it promotes.
	Revision string `json:"revision,omitempty"`
	// Action is promoted, skipped-current or would-promote.
	Action string `json:"action"`
}

// PromoteResult is the outcome of promoting one entry.
type PromoteResult struct {
	Name    string          `json:"name"`
	Kind    string          `json:"kind"`
	Records []PromoteRecord `json:"records,omitempty"`
	// Err carries a per-entry failure in --all summaries.
	Err string `json:"error,omitempty"`
}

// Promote adds a tag to an already-published build instead of rebuilding
// it: in every selected registry it picks the newest tag matching From,
// requires the build's digest to carry a valid signature under that
// registry's signing config, and adds Tag to the same digest. Tags still
// never move: a Tag already on that digest is skipped-current, one on
// another digest fails the entry. Nothing is signed — the build already
// is, and that signature is what was verified.
func (e *Engine) Promote(ctx context.Context, entryName string, opts PromoteOptions) (*PromoteResult, error) {
	entry, err := e.Entry(entryName)
	if err != nil {
		return nil, err
	}
	res := &PromoteResult{Name: entry.Name, Kind: entry.Kind}
	from, err := e.expandField(entry.Name+" --from", opts.From)
	if err != nil {
		return res, err
	}
	if _, err := path.Match(from, ""); err != nil {
		return res, fmt.Errorf("--from %q is not a valid tag pattern: %w", from, err)
	}
	tag, err := e.expandField(entry.Name+" --tag", opts.Tag)
	if err != nil {
		return res, err
	}
	if _, err := name.NewTag("example.invalid/r:"+tag, name.StrictValidation); err != nil {
		return res, fmt.Errorf("--tag %q is not a valid OCI tag", tag)
	}
	regs, err := e.global.SelectRegistries(opts.Registries)
	if err != nil {
		return res, err
	}
	if len(regs) == 0 {
		return res, errors.New("promotion needs at least one registry in the patchy config")
	}
	for _, reg := range regs {
		repo := strings.TrimSuffix(reg.URL, "/") + "/" + entry.Repository
		rec, err := e.promoteIn(ctx, entry, repo, from, tag)
		rec.Registry = reg.Name
		if err != nil {
			return res, fmt.Errorf("%s: %w", reg.Name, err)
		}
		subject := repo + "@" + rec.Digest
		if !e.ensurer.Valid(ctx, subject, reg.EffectiveSigning(&e.global.Signing)) {
			return res, fmt.Errorf("%s: %s carries no valid signature under the %s registry's signing "+
				"config; only a signed build is promoted", reg.Name, rec.Source, reg.Name)
		}
		if rec.Action == "" {
			if opts.DryRun {
				rec.Action = ActionWouldPromote
				e.notef(entry.Name, "promote", "would tag %s as %s", rec.Source, tag)
			} else {
				e.notef(entry.Name, "promote", "tagging %s as %s", rec.Source, tag)
				if err := e.reg.Tag(ctx, subject, tag); err != nil {
					return res, err
				}
				rec.Action = ActionPromoted
			}
		}
		res.Records = append(res.Records, rec)
	}
	return res, nil
}

// promoteIn resolves one registry's promotion: the newest build matching
// from, its digest and revision, and whether tag already holds it (Action
// skipped-current) — an empty Action means the tag is still to be added.
func (e *Engine) promoteIn(ctx context.Context, entry Entry, repo, from, tag string) (PromoteRecord, error) {
	rec := PromoteRecord{Ref: repo + ":" + tag}
	tags, err := e.reg.Tags(ctx, repo)
	if err != nil {
		return rec, err
	}
	source, err := newestMatch(tags, from, tag)
	if err != nil {
		return rec, fmt.Errorf("%s: %w", repo, err)
	}
	rec.Source = repo + ":" + source
	if rec.Digest, err = e.reg.Digest(ctx, rec.Source); err != nil {
		return rec, err
	}
	ann, err := e.reg.Annotations(ctx, rec.Source)
	if err != nil {
		return rec, err
	}
	rec.Revision = ann[fluxartifact.RevisionAnnotation]
	current, exists, err := e.reg.Exists(ctx, rec.Ref)
	switch {
	case err != nil:
		return rec, err
	case exists && current == rec.Digest:
		rec.Action = ActionSkippedCurrent
		e.notef(entry.Name, "promote", "up to date: %s", rec.Ref)
	case exists:
		return rec, fmt.Errorf("%s already points at %s, not %s (%s); tags never move",
			rec.Ref, current, rec.Digest, source)
	}
	return rec, nil
}

// newestMatch picks the newest tag matching pattern, never the target tag
// itself: semver order when every candidate parses as a version
// (X.Y.Z-edge.<timestamp>.g<commit> orders by its timestamp), lexical
// otherwise.
func newestMatch(tags []string, pattern, target string) (string, error) {
	var matches []string
	for _, t := range tags {
		if t == target {
			continue
		}
		if ok, _ := path.Match(pattern, t); ok {
			matches = append(matches, t)
		}
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no tag matches %q", pattern)
	}
	versions := make(map[string]*semver.Version, len(matches))
	for _, m := range matches {
		v, err := semver.NewVersion(m)
		if err != nil {
			versions = nil
			break
		}
		versions[m] = v
	}
	slices.SortFunc(matches, func(a, b string) int {
		if versions != nil {
			return versions[a].Compare(versions[b])
		}
		return strings.Compare(a, b)
	})
	return matches[len(matches)-1], nil
}
