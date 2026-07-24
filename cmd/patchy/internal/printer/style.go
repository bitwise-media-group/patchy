// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package printer

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// Palette. Colours are ANSI 0-15 rather than truecolor so they inherit the
// user's own terminal theme — a finding rendered in a light terminal has to be
// as readable as one in a dark terminal, and only the user knows which.
var (
	styleHeader = lipgloss.NewStyle().Bold(true)
	styleDim    = lipgloss.NewStyle().Faint(true)
	styleKey    = lipgloss.NewStyle().Bold(true)

	styleCritical = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("1")) // red
	styleHigh     = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	styleMedium   = lipgloss.NewStyle().Foreground(lipgloss.Color("3")) // yellow
	styleLow      = lipgloss.NewStyle().Foreground(lipgloss.Color("4")) // blue
	styleGood     = lipgloss.NewStyle().Foreground(lipgloss.Color("2")) // green
	styleBad      = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	styleWaiting  = lipgloss.NewStyle().Foreground(lipgloss.Color("5")) // magenta
)

// statusColumns are the columns whose values carry meaning worth colouring.
// Matching by column name rather than position keeps this working when the CRD
// print columns change.
var statusColumns = map[string]bool{
	"SEVERITY": true, "PRIORITY": true, "PHASE": true,
	"VERDICT": true, "SUCCESS": true, "STATE": true,
}

// styleFor returns the style for a cell value, or nil when it should be left
// alone. Vocabulary comes from api/v1alpha1: Level, Rating, Recommendation,
// RunPhase and the Finding phases.
func styleFor(column, value string) *lipgloss.Style {
	if !statusColumns[strings.ToUpper(column)] {
		return nil
	}
	switch strings.ToLower(value) {
	case "critical":
		return &styleCritical
	case "high":
		return &styleHigh
	case "medium":
		return &styleMedium
	case "low", "none":
		return &styleLow

	// Verdicts.
	case "remediate":
		return &styleGood
	case "manual":
		return &styleWaiting
	case "ignore":
		return &styleDim

	// Terminal phases: reached the intended end, or did not.
	case "remediated", "complete", "true", "merged":
		return &styleGood
	case "failed", "false":
		return &styleBad
	case "dismissed":
		return &styleDim

	// Phases where something is expected of a human.
	case "awaitingapproval", "handedoff", "inreview":
		return &styleWaiting
	}
	return nil
}

// paint applies s to v when styling is on.
func paint(on bool, s lipgloss.Style, v string) string {
	if !on || v == "" {
		return v
	}
	return s.Render(v)
}
