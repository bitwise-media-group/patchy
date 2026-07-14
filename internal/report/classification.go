// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package report

import (
	"errors"
	"fmt"
	"slices"
)

// Recommendation is the classifier's verdict. The values are the same
// vocabulary internal/labels stamps on the issue
// (security-recommendation: <value>) — one vocabulary, no mapping.
type Recommendation string

// The classification verdicts.
const (
	RecommendIgnore    Recommendation = "ignore"
	RecommendRemediate Recommendation = "remediate"
	RecommendManual    Recommendation = "manual"
)

// Level is a priority or severity value.
type Level string

var validLevels = []Level{"low", "medium", "high", "critical"}

// Classification is the parsed classification report.
type Classification struct {
	Recommendation Recommendation `yaml:"recommendation"`
	Priority       Level          `yaml:"priority"`
	Severity       Level          `yaml:"severity"`
	// Confidence is the likelihood the recommendation is right — for
	// remediate, the likelihood of full remediation without breaking
	// functionality. Pointer so absence is detectable.
	Confidence *float64 `yaml:"confidence"`
	// BreakingChangeAvailable marks that a better fix exists but would
	// break external callers; the pipeline then holds for /approve.
	BreakingChangeAvailable bool `yaml:"breaking_change_available"`
	// Model, MaxTurns and TokenBudget are required iff Recommendation is
	// remediate; the caller clamps them against its ceilings/allowlist.
	Model       string `yaml:"model"`
	MaxTurns    int    `yaml:"max_turns"`
	TokenBudget int    `yaml:"token_budget"`
	// Body is the markdown analysis following the frontmatter.
	Body string `yaml:"-"`
}

// ParseClassification parses and validates a classification report.
func ParseClassification(data []byte) (*Classification, error) {
	block, body, err := splitFrontmatter(data)
	if err != nil {
		return nil, err
	}
	var c Classification
	if err := decodeStrict(block, &c); err != nil {
		return nil, err
	}
	c.Body = body
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("report: classification: %w", err)
	}
	return &c, nil
}

func (c *Classification) validate() error {
	var errs []error
	switch c.Recommendation {
	case RecommendIgnore, RecommendRemediate, RecommendManual:
	default:
		errs = append(errs, fmt.Errorf("recommendation %q is not ignore, remediate, or manual", c.Recommendation))
	}
	if !slices.Contains(validLevels, c.Priority) {
		errs = append(errs, fmt.Errorf("priority %q is not low, medium, high, or critical", c.Priority))
	}
	if !slices.Contains(validLevels, c.Severity) {
		errs = append(errs, fmt.Errorf("severity %q is not low, medium, high, or critical", c.Severity))
	}
	switch {
	case c.Confidence == nil:
		errs = append(errs, errors.New("confidence is required"))
	case *c.Confidence < 0 || *c.Confidence > 1:
		errs = append(errs, fmt.Errorf("confidence %v is outside [0, 1]", *c.Confidence))
	}
	if c.Recommendation == RecommendRemediate {
		if c.Model == "" {
			errs = append(errs, errors.New("model is required when recommending remediation"))
		}
		if c.MaxTurns < 1 {
			errs = append(errs, errors.New("max_turns must be a positive integer when recommending remediation"))
		}
		if c.TokenBudget < 1 {
			errs = append(errs, errors.New("token_budget must be a positive integer when recommending remediation"))
		}
	}
	return errors.Join(errs...)
}
