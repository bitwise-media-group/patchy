// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package mirror

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bitwise-media-group/patchy/internal/mirror/imageref"
	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
	"github.com/bitwise-media-group/patchy/internal/mirror/yamledit"
)

// checkTracks computes the cooldown pick for every image a chart declares
// under images.track, without writing. Tracked images ride a release train
// of their own — nothing about the chart version says which tag to mirror,
// and the chart default is often a mutable tag the pipeline must never
// lock.
func (e *Engine) checkTracks(ctx context.Context, entry spec.Entry) ([]TrackPlan, error) {
	m := entry.Chart
	var plans []TrackPlan
	for i, rule := range m.Images.Track {
		plan, err := e.checkTrack(ctx, entry, i, rule)
		if err != nil {
			return nil, err
		}
		plans = append(plans, plan)
	}
	return plans, nil
}

// checkTrack resolves one track rule: current pin from the values file,
// selected tag from the cooldown walk.
func (e *Engine) checkTrack(ctx context.Context, entry spec.Entry, i int, rule spec.Track) (TrackPlan, error) {
	if rule.Image == "" {
		return TrackPlan{}, fmt.Errorf("%s: images.track[%d] missing image", entry.Name, i)
	}
	if rule.ValuesPath == "" {
		return TrackPlan{}, fmt.Errorf("%s: images.track[%d] missing valuesPath", entry.Name, i)
	}
	valuesFile := rule.ValuesFile
	if valuesFile == "" && len(entry.Chart.Discovery.ValuesFiles) > 0 {
		valuesFile = entry.Chart.Discovery.ValuesFiles[0]
	}
	if valuesFile == "" {
		return TrackPlan{}, fmt.Errorf(
			"%s: images.track[%d] has no valuesFile and the chart declares no discovery.valuesFiles", entry.Name, i)
	}
	raw, err := os.ReadFile(filepath.Join(entry.Dir, valuesFile))
	if err != nil {
		return TrackPlan{}, fmt.Errorf("%s: read values file %s: %w", entry.Name, valuesFile, err)
	}
	currentRef, err := yamledit.Get(raw, rule.ValuesPath)
	if err != nil {
		// The tracked pin must already exist there: the pick replaces a
		// value, it never invents one.
		return TrackPlan{}, fmt.Errorf("%s: %q does not resolve in %s: %w", entry.Name, rule.ValuesPath, valuesFile, err)
	}
	parsed, err := imageref.Parse(currentRef)
	if err != nil {
		return TrackPlan{}, fmt.Errorf("%s: current pin %q: %w", entry.Name, currentRef, err)
	}

	cooldownDays := e.global.Update.EffectiveCooldownDays()
	if rule.CooldownDays != nil {
		cooldownDays = *rule.CooldownDays
	}
	candidates, err := e.reg.Tags(ctx, e.rewrite(rule.Image))
	if err != nil {
		return TrackPlan{}, fmt.Errorf("%s: %w", entry.Name, err)
	}
	e.notef(entry.Name, "track", "%s: picking newest tag past the %dd cooldown", rule.Image, cooldownDays)
	selected, err := e.cooldownPick(ctx, entry.Name, rule.Image, candidates, rule.VersionConstraint, cooldownDays)
	if err != nil {
		return TrackPlan{}, fmt.Errorf("%s: %s: %w", entry.Name, rule.Image, err)
	}
	return TrackPlan{
		Image:      rule.Image,
		ValuesFile: valuesFile,
		ValuesPath: rule.ValuesPath,
		Current:    parsed.Tag,
		Selected:   selected,
	}, nil
}

// ApplyTracks re-picks every tracked pin and splices changed picks into
// the values files. Wall-clock dependent — upgrade only, never validate,
// or the byte-identity gate would fail whenever a tag aged past the
// cooldown between commit and CI.
func (e *Engine) ApplyTracks(ctx context.Context, entry spec.Entry) ([]TrackPlan, error) {
	plans, err := e.checkTracks(ctx, entry)
	if err != nil {
		return nil, err
	}
	for _, plan := range plans {
		if !plan.Changed() {
			e.notef(entry.Name, "track", "  %s:%s already pinned", plan.Image, plan.Selected)
			continue
		}
		path := filepath.Join(entry.Dir, plan.ValuesFile)
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		// Replace whatever pin is actually there (already re-verified by
		// Get), not a reconstruction of it.
		oldRef, err := yamledit.Get(raw, plan.ValuesPath)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", entry.Name, err)
		}
		edited, err := yamledit.Set(raw, plan.ValuesPath, oldRef, plan.Image+":"+plan.Selected)
		if err != nil {
			return nil, fmt.Errorf("%s: splice %s: %w", entry.Name, plan.ValuesFile, err)
		}
		if err := os.WriteFile(path, edited, 0o644); err != nil {
			return nil, err
		}
		e.notef(entry.Name, "track", "  %s: %s -> %s (written to %s)",
			plan.Image, plan.Current, plan.Selected, plan.ValuesFile)
	}
	return plans, nil
}
