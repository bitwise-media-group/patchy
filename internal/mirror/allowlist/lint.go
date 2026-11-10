// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package allowlist

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
)

// Issue is one accepted-risk policy violation in an allowlist.
type Issue struct {
	// ID is the offending entry's id (empty when the id itself is missing).
	ID string
	// Expired marks the lapsed-expiry violation. Mirror lint fails on it
	// like any other issue; build reports it and stops suppressing with
	// the entry, so a lapsed acceptance blocks the publish instead.
	Expired bool
	// Message is the human-readable violation.
	Message string
}

// String implements fmt.Stringer.
func (i Issue) String() string { return i.Message }

// Lint enforces the accepted-risk policy: every entry carries an id, a
// statement and a parseable expiry, expiries are in the future (re-review
// on schedule is the whole forcing function) and within maxDays of today.
func Lint(a *spec.Allowlist, today time.Time, maxDays int) []Issue {
	if a == nil {
		return nil
	}
	var issues []Issue
	fail := func(id string, expired bool, format string, args ...any) {
		issues = append(issues, Issue{ID: id, Expired: expired, Message: fmt.Sprintf(format, args...)})
	}
	horizon := today.AddDate(0, 0, maxDays)
	for i, e := range a.Vulnerabilities {
		if e.ID == "" {
			fail("", false, "allowlist entry %d missing id", i)
			continue
		}
		if e.Statement == "" {
			fail(e.ID, false, "allowlist %s missing statement", e.ID)
		}
		if e.ExpiredAt == "" {
			fail(e.ID, false, "allowlist %s missing expired_at", e.ID)
			continue
		}
		expiry, err := time.Parse("2006-01-02", e.ExpiredAt)
		if err != nil {
			fail(e.ID, false, "allowlist %s has unparseable expired_at %q", e.ID, e.ExpiredAt)
			continue
		}
		if !expiry.After(today) {
			fail(e.ID, true, "allowlist %s expired on %s — re-review required", e.ID, e.ExpiredAt)
		}
		if expiry.After(horizon) {
			fail(e.ID, false, "allowlist %s expires more than %d days out", e.ID, maxDays)
		}
	}
	return issues
}

// Active returns the entries still in force today: an expiry that parses
// and lies after today. Suppression with a lapsed or malformed entry would
// accept a risk nobody re-reviewed.
func Active(a *spec.Allowlist, today time.Time) *spec.Allowlist {
	out := &spec.Allowlist{}
	if a == nil {
		return out
	}
	for _, e := range a.Vulnerabilities {
		if expiry, err := time.Parse("2006-01-02", e.ExpiredAt); err == nil && expiry.After(today) {
			out.Vulnerabilities = append(out.Vulnerabilities, e)
		}
	}
	return out
}

// Regenerate derives the allowlist from rows against prev, renders it under
// banner and writes it to path, creating the directory. This is the
// derive-render-write half both generators share; scanning and the policy
// filter that produced rows are the caller's.
func Regenerate(path string, banner Banner, rows []Finding, prev *spec.Allowlist,
	now time.Time, newDays int, preamble string) (Stats, error) {
	entries, stats := Derive(rows, prev, now, newDays)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Stats{}, err
	}
	if err := os.WriteFile(path, RenderBanner(banner, entries, preamble), 0o644); err != nil {
		return Stats{}, fmt.Errorf("write %s: %w", path, err)
	}
	return stats, nil
}
