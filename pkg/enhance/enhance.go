// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package enhance

import (
	"context"

	"github.com/bitwise-media-group/patchy/pkg/source"
)

// Issue is the minimal view of a security-finding issue an Enhancer sees.
type Issue struct {
	Repo   source.Repo
	Number int
	Title  string
	Body   string
	Labels []string
	// CloudResource is set when the finding is about a cloud resource rather
	// than repository code. Enhancers key on it both to skip findings they do
	// not cover and to look the resource up with the platform's own API.
	CloudResource *source.CloudResource
}

// Enrichment is what an Enhancer contributes to an issue.
type Enrichment struct {
	// Owners are GitHub logins responsible for the affected repository, in
	// preference order; the pipeline uses them for issue assignment.
	Owners []string
	// CommentMarkdown is arbitrary content, kept as one sticky comment per
	// enhancer on the tracking issue. Empty means no comment. Semi-structured
	// facts belong in Attributes, not here.
	CommentMarkdown string
	// Attributes are semi-structured facts (system name, environment, tier),
	// projected as tracking labels; carried verbatim.
	Attributes map[string]string
	// Repository is a repository the enhancer resolved for a finding that
	// arrived without one — a cloud finding whose resource carries ownership
	// labels. Ignored when the finding already has a repository: the value is
	// written to the Finding exactly once and never revised, because the
	// rollup ledger, the clone artifact, and the agent Jobs all snapshot it
	// independently. Where several enhancers return one, the first in the
	// chain wins.
	Repository *source.RepositoryRef
}

// Enhancer is the interface a context-enhancement plugin implements.
type Enhancer interface {
	// ID names the enhancer, for logs and comment attribution.
	ID() string
	// Enhance returns the enrichment for the issue, or (nil, nil) when the
	// enhancer has nothing to contribute.
	Enhance(ctx context.Context, issue Issue) (*Enrichment, error)
}
