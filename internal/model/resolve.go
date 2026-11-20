// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package model

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Shipped default model ids (the controllers' flag defaults and the pod-side
// fallback). Family aliases track the newest registered version.
const (
	// DefaultAlias is the default stage model (the analysis and the fallback
	// remediation model).
	DefaultAlias = "anthropic/claude-sonnet"
	// DefaultAllowlist is the default remediation model allowlist.
	DefaultAllowlist = "anthropic/claude-sonnet,anthropic/claude-opus"
)

// claudeIDPattern matches a vendor-bare Anthropic id of the current naming
// scheme: claude-<family>-<major>[-<minor>]. The family is any lower-case word,
// so a future family name parses too. Version parts are capped at two digits so
// a dated snapshot (claude-opus-4-20250514) is not mistaken for a minor release,
// and the old version-first scheme (claude-3-5-sonnet-20241022) never matches
// because its "family" would start with a digit.
var claudeIDPattern = regexp.MustCompile(`^claude-([a-z]+)-([0-9]{1,2})(?:-([0-9]{1,2}))?$`)

// claudeAliasPattern matches a family alias: anthropic/claude-<family>, with
// no version part.
var claudeAliasPattern = regexp.MustCompile(`^anthropic/claude-([a-z]+)$`)

// ParseClaudeID parses a vendor-bare Anthropic model id of the form
// claude-<family>-<major>[-<minor>]. A missing minor is 0. Dated, version-first
// or otherwise unrecognised ids return ok=false.
func ParseClaudeID(bare string) (family string, major, minor int, ok bool) {
	sm := claudeIDPattern.FindStringSubmatch(bare)
	if sm == nil {
		return "", 0, 0, false
	}
	major, _ = strconv.Atoi(sm[2])
	if sm[3] != "" {
		minor, _ = strconv.Atoi(sm[3])
	}
	return sm[1], major, minor, true
}

// IsAlias reports whether id is a family alias (anthropic/claude-<family>):
// a name for "the newest registered version of this family" rather than a
// concrete model.
func IsAlias(id string) bool { return claudeAliasPattern.MatchString(id) }

// Resolve looks id up in models in three tiers, returning the first hit:
//
//  1. exact: a registered model with that canonical id;
//  2. alias: anthropic/claude-<family> resolves to the highest (major, minor)
//     registered Anthropic model of that family, or ok=false when the family
//     has no registered model;
//  3. pattern: a well-formed anthropic/claude-<family>-<major>[-<minor>] with
//     no registry entry is synthesized as a claude-harness-only, unpriced
//     model (see Synthesized), so an Anthropic release patchy has not
//     registered yet still runs.
//
// It is the one lookup every consumer validating a configured or reported
// model id uses.
func Resolve(models []Model, id string) (Model, bool) {
	if m, ok := ModelByID(models, id); ok {
		return m, true
	}
	if sm := claudeAliasPattern.FindStringSubmatch(id); sm != nil {
		return newestOfFamily(models, sm[1])
	}
	return synthesize(id)
}

// Lookup resolves id against the builtin registry: Resolve(Builtins(), id).
func Lookup(id string) (Model, bool) { return Resolve(Builtins(), id) }

// Synthesized reports whether m was synthesized by the pattern tier rather
// than registered: it runs on the claude harness only and carries no pricing.
func Synthesized(m Model) bool { return m.synthesized }

// Concrete maps id to the concrete canonical id it resolves to against the
// builtins, or returns it unchanged when it does not resolve. It is the
// lenient form of Canonicalize for places that must not fail (pod-side
// defaults); controllers validate with Canonicalize at startup.
func Concrete(id string) string {
	if m, ok := Lookup(id); ok {
		return m.ID
	}
	return id
}

// Canonicalize maps every id to the concrete canonical id it resolves to
// against the builtins (aliases become the newest registered version),
// trims blanks, and drops duplicates while keeping first-seen order. It
// errors on an alias of an unknown family and on an id that is neither
// registered nor a well-formed Anthropic version id.
func Canonicalize(ids []string) ([]string, error) {
	var (
		out  []string
		errs []error
	)
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		m, ok := Lookup(id)
		if !ok {
			if IsAlias(id) {
				errs = append(errs, fmt.Errorf("model alias %q names no registered model family", id))
			} else {
				errs = append(errs, fmt.Errorf("unknown model %q (not in the model registry, and not of the form "+
					"anthropic/claude-<family>[-<major>[-<minor>]])", id))
			}
			continue
		}
		if !slices.Contains(out, m.ID) {
			out = append(out, m.ID)
		}
	}
	return out, errors.Join(errs...)
}

// WithResolved returns models plus a synthesized entry for every id that
// resolves only through the pattern tier, so code that iterates a model list
// (provider.EffectiveModelMap deriving Bedrock/Vertex ids) covers those too.
// models itself is not modified.
func WithResolved(models []Model, ids ...string) []Model {
	out := slices.Clone(models)
	for _, id := range ids {
		m, ok := Resolve(out, strings.TrimSpace(id))
		if ok && m.synthesized {
			if _, dup := ModelByID(out, m.ID); !dup {
				out = append(out, m)
			}
		}
	}
	return out
}

// newestOfFamily picks the registered Anthropic model of family with the
// highest (major, minor) version.
func newestOfFamily(models []Model, family string) (Model, bool) {
	var (
		best             Model
		found            bool
		bestMaj, bestMin int
	)
	for _, m := range models {
		if m.ProviderID != ProviderAnthropic {
			continue
		}
		fam, maj, mnr, ok := ParseClaudeID(m.BareID())
		if !ok || fam != family {
			continue
		}
		if !found || maj > bestMaj || (maj == bestMaj && mnr > bestMin) {
			best, found, bestMaj, bestMin = m, true, maj, mnr
		}
	}
	return best, found
}

// synthesize builds the pattern-tier model for a well-formed but unregistered
// Anthropic id: claude harness only (its bare id is what the claude CLI's
// --model takes) and no pricing. The claude CLI reports its own cost, so the
// token-priced fallback is never needed for it.
func synthesize(id string) (Model, bool) {
	prov, bare, ok := strings.Cut(id, "/")
	if !ok || prov != ProviderAnthropic {
		return Model{}, false
	}
	family, major, minor, ok := ParseClaudeID(bare)
	if !ok {
		return Model{}, false
	}
	version := strconv.Itoa(major)
	if strings.Count(bare, "-") == 3 {
		version += "." + strconv.Itoa(minor)
	}
	return Model{
		ID:          id,
		ProviderID:  ProviderAnthropic,
		Name:        "Claude " + strings.ToUpper(family[:1]) + family[1:] + " " + version,
		Supported:   map[string]string{HarnessClaude: bare},
		Preferred:   HarnessClaude,
		synthesized: true,
	}, true
}
