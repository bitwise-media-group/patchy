// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package report

import (
	"errors"
	"fmt"
	"slices"
)

// Recommendation is the investigation's verdict. The values are the same
// vocabulary internal/labels stamps on the tracking issue
// (security-recommendation: <value>) — one vocabulary, no mapping.
type Recommendation string

// The investigation verdicts.
const (
	RecommendIgnore    Recommendation = "ignore"
	RecommendRemediate Recommendation = "remediate"
	RecommendManual    Recommendation = "manual"
)

// Level is a priority or severity value.
type Level string

var validLevels = []Level{"low", "medium", "high", "critical"}

// Rating grades one investigation dimension. "none" is an assessed
// "not applicable / no realistic risk" — distinct from an absent rating,
// which fails validation.
type Rating string

// validRatings is the rating vocabulary.
var validRatings = []Rating{"none", "low", "medium", "high", "critical"}

// Analysis is one investigation dimension: the rating plus a short
// justification (the full reasoning lives in the report body).
type Analysis struct {
	Rating  Rating `yaml:"rating"`
	Summary string `yaml:"summary"`
}

// Investigation is the parsed investigation report — the analysis stage's
// contract, carrying the three assessed dimensions the scheduler's priority
// derives from.
type Investigation struct {
	// Exploitability: can the vulnerability actually be exercised here?
	Exploitability Analysis `yaml:"exploitability"`
	// Likelihood: how probable is exploitation in this deployment?
	Likelihood Analysis `yaml:"likelihood"`
	// Impact: what is the blast radius if exploited?
	Impact Analysis `yaml:"impact"`

	Recommendation Recommendation `yaml:"recommendation"`
	Priority       Level          `yaml:"priority"`
	Severity       Level          `yaml:"severity"`
	// Confidence is the likelihood the recommendation is right — for
	// remediate, the likelihood of full remediation without breaking
	// functionality. Pointer so absence is detectable.
	Confidence *float64 `yaml:"confidence"`
	// BreakingChangeAvailable marks that a better fix exists but would
	// break external callers; the pipeline then holds for approval.
	BreakingChangeAvailable bool `yaml:"breaking_change_available"`
	// Model, MaxTurns and TokenBudget are required iff Recommendation is
	// remediate; the controller clamps them against its ceilings/allowlist.
	Model       string `yaml:"model"`
	MaxTurns    int    `yaml:"max_turns"`
	TokenBudget int    `yaml:"token_budget"`
	// Body is the markdown analysis following the frontmatter.
	Body string `yaml:"-"`
}

// ParseInvestigation parses and validates an investigation report.
func ParseInvestigation(data []byte) (*Investigation, error) {
	block, body, err := splitFrontmatter(data)
	if err != nil {
		return nil, err
	}
	var inv Investigation
	if err := decodeStrict(block, &inv); err != nil {
		return nil, err
	}
	inv.Body = body
	if err := inv.validate(); err != nil {
		return nil, fmt.Errorf("report: investigation: %w", err)
	}
	return &inv, nil
}

func (inv *Investigation) validate() error {
	var errs []error
	for _, dim := range []struct {
		name string
		a    Analysis
	}{
		{"exploitability", inv.Exploitability},
		{"likelihood", inv.Likelihood},
		{"impact", inv.Impact},
	} {
		if !slices.Contains(validRatings, dim.a.Rating) {
			errs = append(errs, fmt.Errorf("%s rating %q is not none, low, medium, high, or critical",
				dim.name, dim.a.Rating))
		}
	}
	switch inv.Recommendation {
	case RecommendIgnore, RecommendRemediate, RecommendManual:
	default:
		errs = append(errs, fmt.Errorf("recommendation %q is not ignore, remediate, or manual", inv.Recommendation))
	}
	if !slices.Contains(validLevels, inv.Priority) {
		errs = append(errs, fmt.Errorf("priority %q is not low, medium, high, or critical", inv.Priority))
	}
	if !slices.Contains(validLevels, inv.Severity) {
		errs = append(errs, fmt.Errorf("severity %q is not low, medium, high, or critical", inv.Severity))
	}
	switch {
	case inv.Confidence == nil:
		errs = append(errs, errors.New("confidence is required"))
	case *inv.Confidence < 0 || *inv.Confidence > 1:
		errs = append(errs, fmt.Errorf("confidence %v is outside [0, 1]", *inv.Confidence))
	}
	if inv.Recommendation == RecommendRemediate {
		if inv.Model == "" {
			errs = append(errs, errors.New("model is required when recommending remediation"))
		}
		if inv.MaxTurns < 1 {
			errs = append(errs, errors.New("max_turns must be a positive integer when recommending remediation"))
		}
		if inv.TokenBudget < 1 {
			errs = append(errs, errors.New("token_budget must be a positive integer when recommending remediation"))
		}
	}
	return errors.Join(errs...)
}
