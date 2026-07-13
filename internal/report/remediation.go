// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package report

import (
	"errors"
	"fmt"
)

// Remediation is the parsed remediation report.
type Remediation struct {
	// Success reports whether the agent believes the finding is fully
	// remediated. Pointer so absence is detectable; the runner verifies the
	// repository state regardless and may downgrade a claimed success.
	Success *bool `yaml:"success"`
	// Confidence is how confident the agent is that the issue is
	// remediated without breaking functionality.
	Confidence *float64 `yaml:"confidence"`
	// Body is the markdown summary following the frontmatter.
	Body string `yaml:"-"`
}

// ParseRemediation parses and validates a remediation report.
func ParseRemediation(data []byte) (*Remediation, error) {
	block, body, err := splitFrontmatter(data)
	if err != nil {
		return nil, err
	}
	var r Remediation
	if err := decodeStrict(block, &r); err != nil {
		return nil, err
	}
	r.Body = body
	if err := r.validate(); err != nil {
		return nil, fmt.Errorf("report: remediation: %w", err)
	}
	return &r, nil
}

func (r *Remediation) validate() error {
	var errs []error
	if r.Success == nil {
		errs = append(errs, errors.New("success is required"))
	}
	switch {
	case r.Confidence == nil:
		errs = append(errs, errors.New("confidence is required"))
	case *r.Confidence < 0 || *r.Confidence > 1:
		errs = append(errs, fmt.Errorf("confidence %v is outside [0, 1]", *r.Confidence))
	}
	return errors.Join(errs...)
}
