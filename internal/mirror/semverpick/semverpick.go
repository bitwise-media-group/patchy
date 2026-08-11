// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package semverpick

import (
	"fmt"
	"regexp"
	"sort"
	"time"

	"github.com/Masterminds/semver/v3"
)

// releaseRE admits exactly the versions the mirror ships: X.Y.Z with an
// optional leading v. Pre-releases, build metadata, moving aliases like
// "latest", and cosign's sha256-* referrer tags all fail it.
var releaseRE = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+$`)

// maxCooldownCandidates bounds the newest-first cooldown walk. Walking every
// tag of a busy repository would be a lot of manifest fetches; a tag old
// enough to clear the cooldown is always within the first few.
const maxCooldownCandidates = 25

// Releases filters candidates down to release versions and returns them
// sorted newest first. The returned strings are the original tags (any v
// prefix intact); ordering compares the parsed versions.
func Releases(candidates []string) []string {
	var out []string
	for _, c := range candidates {
		if releaseRE.MatchString(c) {
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return semver.MustParse(out[i]).GreaterThan(semver.MustParse(out[j]))
	})
	return out
}

// Satisfies reports whether version (a release, optionally v-prefixed)
// satisfies constraint. An empty constraint admits everything.
func Satisfies(version, constraint string) (bool, error) {
	v, err := semver.NewVersion(version)
	if err != nil {
		return false, fmt.Errorf("parse version %q: %w", version, err)
	}
	if constraint == "" {
		return true, nil
	}
	c, err := semver.NewConstraint(constraint)
	if err != nil {
		return false, fmt.Errorf("parse constraint %q: %w", constraint, err)
	}
	return c.Check(v), nil
}

// Newest returns the newest release among candidates that satisfies
// constraint and is strictly newer than current, or ("", false) when the
// current pin is already the best available. Non-release candidates are
// skipped; current must itself parse as a version.
func Newest(current string, candidates []string, constraint string) (string, bool, error) {
	best, err := semver.NewVersion(current)
	if err != nil {
		return "", false, fmt.Errorf("parse current version %q: %w", current, err)
	}
	var c *semver.Constraints
	if constraint != "" {
		if c, err = semver.NewConstraint(constraint); err != nil {
			return "", false, fmt.Errorf("parse constraint %q: %w", constraint, err)
		}
	}
	found := ""
	for _, cand := range candidates {
		if !releaseRE.MatchString(cand) {
			continue
		}
		v := semver.MustParse(cand)
		if c != nil && !c.Check(v) {
			continue
		}
		if v.GreaterThan(best) {
			best, found = v, cand
		}
	}
	return found, found != "", nil
}

// CreatedFunc reports when a tag was published. ok=false means the registry
// records no creation timestamp for the tag; the walk skips it with a
// warning rather than failing.
type CreatedFunc func(tag string) (created time.Time, ok bool, err error)

// CooldownPick walks release candidates newest-first and returns the first
// tag published at least cooldown before now, so a release yanked or
// hot-fixed within its soak period is never the one the mirror adopts.
// Tags failing constraint are skipped without counting against the
// candidate cap; skipped-inside-cooldown tags are reported through onSkip
// (nil is fine). A cooldown of zero picks the newest satisfying tag.
func CooldownPick(candidates []string, constraint string, cooldown time.Duration,
	now time.Time, created CreatedFunc, onSkip func(tag, reason string)) (string, error) {
	if onSkip == nil {
		onSkip = func(string, string) {}
	}
	var c *semver.Constraints
	if constraint != "" {
		var err error
		if c, err = semver.NewConstraint(constraint); err != nil {
			return "", fmt.Errorf("parse constraint %q: %w", constraint, err)
		}
	}
	cutoff := now.Add(-cooldown)
	examined := 0
	for _, tag := range Releases(candidates) {
		if c != nil && !c.Check(semver.MustParse(tag)) {
			continue
		}
		examined++
		if examined > maxCooldownCandidates {
			return "", fmt.Errorf("no tag cleared the %s cooldown within the newest %d in-constraint tags",
				cooldown, maxCooldownCandidates)
		}
		ts, ok, err := created(tag)
		if err != nil {
			return "", fmt.Errorf("resolve creation time of tag %q: %w", tag, err)
		}
		if !ok {
			onSkip(tag, "no creation timestamp")
			continue
		}
		if !ts.After(cutoff) {
			return tag, nil
		}
		onSkip(tag, fmt.Sprintf("published %s, inside the cooldown", ts.UTC().Format("2006-01-02")))
	}
	return "", fmt.Errorf("no tag satisfies the constraint and the %s cooldown", cooldown)
}
