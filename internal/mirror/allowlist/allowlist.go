// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package allowlist

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
)

// Finding is one blocking (already severity-filtered) scan finding for one
// image. The same ID appears once per image reporting it; derivation
// aggregates them.
type Finding struct {
	// ID is the primary identifier the scanner reported (CVE, GHSA, GO-...).
	ID string
	// Package is the affected artifact name.
	Package string
	// Installed is the version the locked image ships.
	Installed string
	// FixedIn lists the advisory's fixed versions across release branches.
	FixedIn []string
}

// Stats summarizes one derivation for narration.
type Stats struct {
	Kept    int
	Added   int
	Dropped []string
}

// Derive regenerates the allowlist entries from findings against the
// previous file. Entries come out sorted by ID; surviving IDs keep their
// previous expiry and notes, new IDs expire newDays after today, and
// previous IDs no longer reported are dropped (returned in Stats).
func Derive(findings []Finding, prev *spec.Allowlist, today time.Time, newDays int) ([]spec.AllowlistEntry, Stats) {
	byID := map[string][]Finding{}
	for _, f := range findings {
		byID[f.ID] = append(byID[f.ID], f)
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	prevByID := map[string]spec.AllowlistEntry{}
	if prev != nil {
		for _, e := range prev.Vulnerabilities {
			if _, ok := prevByID[e.ID]; !ok {
				prevByID[e.ID] = e
			}
		}
	}

	newExpiry := today.AddDate(0, 0, newDays).UTC().Format("2006-01-02")
	entries := make([]spec.AllowlistEntry, 0, len(ids))
	var stats Stats
	for _, id := range ids {
		entry := spec.AllowlistEntry{ID: id, Statement: statement(byID[id])}
		if prevEntry, ok := prevByID[id]; ok && prevEntry.ExpiredAt != "" {
			entry.ExpiredAt = prevEntry.ExpiredAt
			entry.Notes = prevEntry.Notes
			stats.Kept++
		} else {
			entry.ExpiredAt = newExpiry
			if prevEntry, ok := prevByID[id]; ok {
				entry.Notes = prevEntry.Notes
			}
			stats.Added++
		}
		entries = append(entries, entry)
	}
	for _, e := range prevOrder(prev) {
		if _, ok := byID[e]; !ok {
			stats.Dropped = append(stats.Dropped, e)
		}
	}
	return entries, stats
}

// prevOrder lists previous IDs in file order, deduplicated.
func prevOrder(prev *spec.Allowlist) []string {
	if prev == nil {
		return nil
	}
	seen := map[string]bool{}
	var ids []string
	for _, e := range prev.Vulnerabilities {
		if e.ID != "" && !seen[e.ID] {
			seen[e.ID] = true
			ids = append(ids, e.ID)
		}
	}
	return ids
}

// prereleaseRE drops fixed-version candidates from branches the mirror will
// never ship (the pipeline only ships releases).
var prereleaseRE = regexp.MustCompile(`(?i)-(rc|alpha|beta|pre|dev)`)

// statement builds the machine-fact statement for one finding ID across
// the images reporting it: the affected packages, the minimal released
// upgrade that clears it, and what the locked images actually ship.
func statement(rows []Finding) string {
	pkgs := uniqueSorted(rows, func(f Finding) string { return f.Package }, sort.Strings)
	installed := uniqueSorted(rows, func(f Finding) string { return f.Installed }, sortVersions)

	var fixes []string
	seenFix := map[string]bool{}
	for _, f := range rows {
		for _, fix := range f.FixedIn {
			for v := range strings.FieldsSeq(fix) {
				if v == "" || prereleaseRE.MatchString(v) || seenFix[v] {
					continue
				}
				seenFix[v] = true
				fixes = append(fixes, v)
			}
		}
	}
	sortVersions(fixes)

	// Advisories often list a chain of fixed-in versions across several
	// release branches. The useful number is the lowest release ABOVE
	// what the image ships — the minimal upgrade that clears it — not the
	// newest in the chain. Normalise the installed version for comparison
	// only (v1.2.3, go1.26.4, 28.5.2+incompatible); the statement still
	// quotes what the scanner reported.
	maxInstalled := ""
	for _, v := range installed {
		n := normalizeVersion(v)
		if maxInstalled == "" || versionLess(maxInstalled, n) {
			maxInstalled = n
		}
	}
	fixed := ""
	for _, v := range fixes {
		if versionLess(maxInstalled, v) {
			fixed = v
			break
		}
	}
	if fixed == "" && len(fixes) > 0 {
		// No listed fix above what is installed (a fix only on an older
		// branch): the newest in the chain beats claiming nothing exists.
		fixed = fixes[len(fixes)-1]
	}
	if fixed == "" {
		fixed = "an unreleased version"
	}

	return fmt.Sprintf("Fixed in %s %s; the locked image ships %s. "+
		"Derived from the image scan at bump time; accepted in PR review.",
		strings.Join(pkgs, "/"), fixed, strings.Join(installed, "/"))
}

// uniqueSorted collects one field across rows, deduplicated and sorted.
func uniqueSorted(rows []Finding, field func(Finding) string, sortFn func([]string)) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range rows {
		v := field(r)
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sortFn(out)
	return out
}

// normalizeVersion strips a leading v/go prefix and any +suffix for
// comparison.
func normalizeVersion(v string) string {
	v = strings.TrimPrefix(v, "go")
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i]
	}
	return v
}

// sortVersions sorts version-ish strings ascending, GNU sort -V style.
func sortVersions(vs []string) {
	sort.SliceStable(vs, func(i, j int) bool { return versionLess(vs[i], vs[j]) })
}

// versionLess compares two strings the way GNU version sort does:
// alternating non-digit runs compare byte-wise and digit runs compare
// numerically.
func versionLess(a, b string) bool {
	for a != "" || b != "" {
		an, bn := nonDigitLen(a), nonDigitLen(b)
		if c := strings.Compare(a[:an], b[:bn]); c != 0 {
			return c < 0
		}
		a, b = a[an:], b[bn:]
		ad, bd := digitLen(a), digitLen(b)
		if c := compareNumeric(a[:ad], b[:bd]); c != 0 {
			return c < 0
		}
		a, b = a[ad:], b[bd:]
	}
	return false
}

// nonDigitLen measures the leading non-digit run.
func nonDigitLen(s string) int {
	i := 0
	for i < len(s) && (s[i] < '0' || s[i] > '9') {
		i++
	}
	return i
}

// digitLen measures the leading digit run.
func digitLen(s string) int {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return i
}

// compareNumeric compares two digit runs numerically without overflow.
func compareNumeric(a, b string) int {
	a = strings.TrimLeft(a, "0")
	b = strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}
