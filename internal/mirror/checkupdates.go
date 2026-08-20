// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package mirror

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"

	"github.com/bitwise-media-group/patchy/internal/mirror/semverpick"
	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
)

// UpdatePlan is the read-only answer to "what would upgrade do": one group
// per lockstep set (solo entries form singleton groups), only groups with
// work included. Empty Groups means the whole store is current.
type UpdatePlan struct {
	Groups []UpdateGroup `json:"groups"`
}

// UpdateGroup is one update unit: every member bumps to Target in one PR.
type UpdateGroup struct {
	// Group is the lockstep name, or the entry name for solo entries.
	Group string `json:"group"`
	// Target is the group target version: the LOWEST of the members'
	// newest satisfying versions — upstream publishes lockstep members
	// near-simultaneously, so the bump holds until every member carries
	// the tag.
	Target string `json:"target"`
	// Members lists every entry in the group, including ones already at
	// Target (their tracked images may still move).
	Members []MemberPlan `json:"members"`
}

// MemberPlan is one entry's part in a group update.
type MemberPlan struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Current is the pinned version.
	Current string `json:"current"`
	// Newest is the newest satisfying upstream version for this member
	// alone (>= Current).
	Newest string `json:"newest"`
	// Target echoes the group target this member will move to.
	Target string `json:"target"`
	// TrackedImages reports cooldown picks for the entry's tracked
	// images; entries appear even when the pick equals the current pin.
	TrackedImages []TrackPlan `json:"trackedImages,omitempty"`
}

// TrackPlan is one tracked image's cooldown pick.
type TrackPlan struct {
	Image      string `json:"image"`
	ValuesFile string `json:"valuesFile"`
	ValuesPath string `json:"valuesPath"`
	Current    string `json:"current"`
	Selected   string `json:"selected"`
	// TagOnly means the values pin is a bare tag (the chart splits
	// repository from tag), so the pick splices just the tag.
	TagOnly bool `json:"tagOnly,omitempty"`
}

// Changed reports whether the pick moves the pin.
func (t TrackPlan) Changed() bool { return t.Selected != t.Current }

// CheckUpdates computes the update plan for the given entries without
// touching the tree: newest satisfying versions, lockstep group targets,
// and tracked-image cooldown picks.
func (e *Engine) CheckUpdates(ctx context.Context, entries []spec.Entry) (*UpdatePlan, error) {
	groups := map[string][]MemberPlan{}
	order := []string{}
	for _, entry := range entries {
		member, err := e.checkEntry(ctx, entry)
		if err != nil {
			return nil, err
		}
		key := entry.Lockstep()
		if key == "" {
			key = entry.Name
		}
		if _, seen := groups[key]; !seen {
			order = append(order, key)
		}
		groups[key] = append(groups[key], member)
	}
	sort.Strings(order)

	plan := &UpdatePlan{Groups: []UpdateGroup{}}
	for _, key := range order {
		members := groups[key]
		target, err := groupTarget(members)
		if err != nil {
			return nil, fmt.Errorf("group %s: %w", key, err)
		}
		hasWork := false
		for i := range members {
			members[i].Target = target
			if members[i].Current != target || anyTrackChanged(members[i].TrackedImages) {
				hasWork = true
			}
		}
		if hasWork {
			plan.Groups = append(plan.Groups, UpdateGroup{Group: key, Target: target, Members: members})
		}
	}
	return plan, nil
}

// anyTrackChanged reports whether any tracked pick moves.
func anyTrackChanged(tracks []TrackPlan) bool {
	for _, t := range tracks {
		if t.Changed() {
			return true
		}
	}
	return false
}

// groupTarget is the lowest of the members' newest satisfying versions.
func groupTarget(members []MemberPlan) (string, error) {
	target := ""
	var targetV *semver.Version
	for _, m := range members {
		v, err := semver.NewVersion(m.Newest)
		if err != nil {
			return "", fmt.Errorf("member %s newest %q: %w", m.Name, m.Newest, err)
		}
		if targetV == nil || v.LessThan(targetV) {
			target, targetV = m.Newest, v
		}
	}
	return target, nil
}

// checkEntry resolves one entry's newest satisfying version and tracked
// picks.
func (e *Engine) checkEntry(ctx context.Context, entry spec.Entry) (MemberPlan, error) {
	member := MemberPlan{
		Name:    entry.Name,
		Kind:    entry.Kind,
		Current: entry.Version(),
		Newest:  entry.Version(),
	}
	candidates, err := e.versionCandidates(ctx, entry)
	if err != nil {
		return MemberPlan{}, fmt.Errorf("%s: %w", entry.Name, err)
	}

	if entry.Kind == spec.KindArtifact && e.artifactCooldown(entry) > 0 {
		// Artifact version picks soak like tracked tags: the newest tag
		// old enough to have survived a yank window.
		picked, err := e.cooldownPick(ctx, entry.Name, entry.Artifact.Artifact.Ref,
			candidates, entry.VersionConstraint(), e.artifactCooldown(entry))
		if err != nil {
			return MemberPlan{}, fmt.Errorf("%s: %w", entry.Name, err)
		}
		if newerThan(picked, member.Current) {
			member.Newest = picked
		}
	} else {
		newest, ok, err := semverpick.Newest(member.Current, candidates, entry.VersionConstraint())
		if err != nil {
			return MemberPlan{}, fmt.Errorf("%s: %w", entry.Name, err)
		}
		if ok {
			member.Newest = newest
		}
	}
	if member.Newest != member.Current {
		e.notef(entry.Name, "check", "update available: %s -> %s", member.Current, member.Newest)
	} else {
		e.notef(entry.Name, "check", "already current (%s)", member.Current)
	}

	if entry.Kind == spec.KindChart {
		tracks, err := e.checkTracks(ctx, entry)
		if err != nil {
			return MemberPlan{}, err
		}
		member.TrackedImages = tracks
	}
	return member, nil
}

// newerThan reports a > b for two parseable versions (false otherwise).
func newerThan(a, b string) bool {
	av, err1 := semver.NewVersion(a)
	bv, err2 := semver.NewVersion(b)
	return err1 == nil && err2 == nil && av.GreaterThan(bv)
}

// versionCandidates lists an entry's upstream version candidates.
func (e *Engine) versionCandidates(ctx context.Context, entry spec.Entry) ([]string, error) {
	if entry.Kind == spec.KindArtifact {
		return e.reg.Tags(ctx, e.rewrite(entry.Artifact.Artifact.Ref))
	}
	repo := entry.Chart.Chart.Repo
	if after, ok := strings.CutPrefix(repo, "oci://"); ok {
		path := after + "/" + entry.Chart.Chart.Name
		return e.reg.Tags(ctx, e.rewrite(path))
	}
	return e.puller.Versions(ctx, repo, entry.Chart.Chart.Name)
}

// artifactCooldown resolves an artifact's version soak window in days.
func (e *Engine) artifactCooldown(entry spec.Entry) int {
	if cd := entry.Artifact.Artifact.CooldownDays; cd != nil {
		return *cd
	}
	return e.global.Update.EffectiveCooldownDays()
}

// cooldownPick walks candidates newest-first for the first tag older than
// the cooldown, narrating skips.
func (e *Engine) cooldownPick(ctx context.Context, entryName, repo string,
	candidates []string, constraint string, cooldownDays int) (string, error) {
	cooldown := time.Duration(cooldownDays) * 24 * time.Hour
	pullRepo := e.rewrite(repo)
	created := func(tag string) (time.Time, bool, error) {
		ts, ok, err := e.reg.Created(ctx, pullRepo+":"+tag)
		if err != nil {
			return time.Time{}, false, err
		}
		return ts, ok, nil
	}
	onSkip := func(tag, reason string) {
		e.notef(entryName, "check", "  skipping %s:%s (%s)", repo, tag, reason)
	}
	return semverpick.CooldownPick(candidates, constraint, cooldown, e.now(), created, onSkip)
}
