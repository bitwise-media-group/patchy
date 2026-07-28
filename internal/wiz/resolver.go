// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package wiz

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/bitwise-media-group/patchy/internal/wizapi"
	"github.com/bitwise-media-group/patchy/pkg/source"
)

// IssueRejecter is the slice of the Wiz API the resolver needs. wizapi.Client
// implements it; tests substitute a fake.
type IssueRejecter interface {
	RejectIssue(ctx context.Context, issueID, reason, note string) error
}

// Resolver is the Wiz Issues write-back: an ignore verdict rejects the
// originating Wiz issue with a note, the cloud analogue of dismissing a
// code-scanning alert. It exists only when the Integration configures
// spec.wiz.api; Defend threats have no write-back — a runtime detection is a
// record of something that happened, not a state to dismiss.
type Resolver struct {
	api IssueRejecter
}

var _ source.Resolver = (*Resolver)(nil)

// NewResolver builds the write-back over a Wiz API client.
func NewResolver(api IssueRejecter) *Resolver { return &Resolver{api: api} }

// Resolve implements source.Resolver. Each alert id is a Wiz issue id; every
// issue folded into the finding is rejected, and one failure does not stop
// the rest.
func (r *Resolver) Resolve(ctx context.Context, alerts []source.AlertRef, v source.Verdict) error {
	if v.Kind != source.VerdictIgnore {
		return nil
	}
	reason := wizapi.ResolutionWontFix
	if strings.Contains(strings.ToLower(v.Reason), "false positive") {
		reason = wizapi.ResolutionFalsePositive
	}
	var errs []error
	for _, a := range alerts {
		if a.ID == "" {
			continue
		}
		if err := r.api.RejectIssue(ctx, a.ID, reason, v.Comment); err != nil {
			errs = append(errs, fmt.Errorf("reject wiz issue %s: %w", a.ID, err))
		}
	}
	return errors.Join(errs...)
}
