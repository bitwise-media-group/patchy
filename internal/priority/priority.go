// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package priority maps investigation results onto the 0–100 scheduling
// priority remediation runs compete with. The controller, not the agent, owns
// the ranking, so weights are tunable without re-prompting. The agent's own
// priority level remains a display field.
package priority

import v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"

// Weights distribute the score across the assessed dimensions; they should
// sum to 1 (Score normalizes regardless).
type Weights struct {
	Severity       float64
	Exploitability float64
	Likelihood     float64
	Impact         float64
}

// DefaultWeights favor severity and exploitability, per the design.
var DefaultWeights = Weights{Severity: 0.3, Exploitability: 0.3, Likelihood: 0.2, Impact: 0.2}

// levelValue maps a Level onto 0..3.
func levelValue(l v1alpha1.Level) float64 {
	switch l {
	case v1alpha1.LevelLow:
		return 0
	case v1alpha1.LevelMedium:
		return 1
	case v1alpha1.LevelHigh:
		return 2
	case v1alpha1.LevelCritical:
		return 3
	default:
		return 0
	}
}

// ratingValue maps a Rating onto 0..3, evenly spreading the five-value
// vocabulary over the same span as levelValue (none and unassessed, the empty
// string, are 0).
func ratingValue(r v1alpha1.Rating) float64 {
	switch r {
	case v1alpha1.RatingLow:
		return 0.75
	case v1alpha1.RatingMedium:
		return 1.5
	case v1alpha1.RatingHigh:
		return 2.25
	case v1alpha1.RatingCritical:
		return 3
	default:
		return 0
	}
}

// Score computes the scheduling priority in [0, 100] from the scanner
// severity and the investigation's three dimension ratings.
func Score(severity v1alpha1.Level, exploitability, likelihood, impact v1alpha1.Rating, w Weights) int32 {
	total := w.Severity + w.Exploitability + w.Likelihood + w.Impact
	if total <= 0 {
		w, total = DefaultWeights, 1
	}
	weighted := w.Severity*levelValue(severity) +
		w.Exploitability*ratingValue(exploitability) +
		w.Likelihood*ratingValue(likelihood) +
		w.Impact*ratingValue(impact)
	score := int32(weighted/total/3*100 + 0.5)
	return min(max(score, 0), 100)
}
