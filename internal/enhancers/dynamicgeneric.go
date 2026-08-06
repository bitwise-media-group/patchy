// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package enhancers

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/bitwise-media-group/patchy/internal/generic"
	"github.com/bitwise-media-group/patchy/pkg/enhance"
	pkggeneric "github.com/bitwise-media-group/patchy/pkg/generic"
	"github.com/bitwise-media-group/patchy/pkg/source"
)

// AttributedEnrichment is one enrichment with its own attribution: what a
// multi-instance enhancer yields, because one chain entry can speak for
// several identities.
type AttributedEnrichment struct {
	// ID attributes the enrichment — the label on the sticky comment and
	// the tie-breaker in attribute precedence.
	ID string
	// Enrichment is the contribution; never nil.
	Enrichment *enhance.Enrichment
}

// MultiEnhancer is the internal seam for a chain entry carrying several
// identities — N generic integrations behind one enhancer. The chain runner
// type-asserts for it and prefers it over Enhance, so each identity's
// enrichment stays separately attributed. It is deliberately internal:
// pkg/enhance stays a one-plugin-one-identity contract.
type MultiEnhancer interface {
	// EnhanceAll returns every identity's contribution, in precedence
	// order. Partial results beside a non-nil error are meaningful: the
	// runner records both, so one broken endpoint does not discard the
	// others' work.
	EnhanceAll(ctx context.Context, issue enhance.Issue) ([]AttributedEnrichment, error)
}

// GenericConfig is one generic Integration's enhancer endpoint, as read off
// the CR (and its Secret) at enhance time.
type GenericConfig struct {
	// Name is the Integration's name — the enrichment attribution.
	Name string
	// URL the enhancement request POSTs to.
	URL string
	// Timeout bounds the call; zero means the client default.
	Timeout time.Duration
	// Secret signs the request body. Left nil when the Secret could not be
	// read, so the failure surfaces on this integration's call rather than
	// taking the whole fan-out down.
	Secret []byte
}

// GenericConfigSource yields every enabled generic enhancer endpoint. Empty
// means the capability is off everywhere (the enhancer stands aside); an
// error means the list could not be read (the finding is held and retried).
type GenericConfigSource func(ctx context.Context) ([]GenericConfig, error)

// DynamicGeneric fans one enhancement out to every enabled generic
// integration's endpoint, each call signed with that integration's secret
// and bounded by its own timeout. Configuration is read per enhancement, the
// same rule as the cloud wrappers; unlike them there is no client state to
// memoize — a signed POST is built from scratch every time — so there is no
// Close.
type DynamicGeneric struct {
	// Configs reads the enabled endpoints. Required.
	Configs GenericConfigSource
	// Do performs one enhancement call; nil means the internal/generic
	// client. The seam exists for tests, which must not dial anything.
	Do func(ctx context.Context, cfg GenericConfig, req pkggeneric.EnhanceRequest) (*pkggeneric.EnhanceResponse, error)
}

var (
	_ enhance.Enhancer = (*DynamicGeneric)(nil)
	_ MultiEnhancer    = (*DynamicGeneric)(nil)
)

// ID implements enhance.Enhancer. The id never attributes an enrichment —
// EnhanceAll attributes each to its integration — it only names the chain
// entry in logs.
func (*DynamicGeneric) ID() string { return "generic" }

// Enhance implements enhance.Enhancer vacuously: the chain runner prefers
// EnhanceAll on anything implementing MultiEnhancer, so this never runs.
func (*DynamicGeneric) Enhance(context.Context, enhance.Issue) (*enhance.Enrichment, error) {
	return nil, nil
}

// EnhanceAll implements MultiEnhancer: one signed POST per enabled
// integration, sorted by name so precedence (first repository wins, first
// attribute wins) is deterministic rather than list-order luck. Per-endpoint
// failures are joined and returned beside the successes.
func (d *DynamicGeneric) EnhanceAll(ctx context.Context, issue enhance.Issue) ([]AttributedEnrichment, error) {
	cfgs, err := d.Configs(ctx)
	if err != nil {
		return nil, fmt.Errorf("generic: read integration config: %w", err)
	}
	slices.SortFunc(cfgs, func(a, b GenericConfig) int {
		return strings.Compare(a.Name, b.Name)
	})
	var out []AttributedEnrichment
	var errs []error
	for _, cfg := range cfgs {
		resp, err := d.call(ctx, cfg, issue)
		if err != nil {
			errs = append(errs, fmt.Errorf("generic %s: %w", cfg.Name, err))
			continue
		}
		if resp == nil {
			continue // nothing to contribute
		}
		out = append(out, AttributedEnrichment{ID: cfg.Name, Enrichment: toEnrichment(resp)})
	}
	return out, errors.Join(errs...)
}

// call performs one endpoint's enhancement.
func (d *DynamicGeneric) call(
	ctx context.Context, cfg GenericConfig, issue enhance.Issue,
) (*pkggeneric.EnhanceResponse, error) {
	req := pkggeneric.EnhanceRequest{
		Version:     pkggeneric.Version,
		Integration: cfg.Name,
		Issue:       toWireIssue(issue),
	}
	if d.Do != nil {
		return d.Do(ctx, cfg, req)
	}
	c, err := generic.NewClient(generic.ClientOptions{
		URL:     cfg.URL,
		Secret:  cfg.Secret,
		Timeout: cfg.Timeout,
	})
	if err != nil {
		return nil, err
	}
	return c.Enhance(ctx, req)
}

// toWireIssue maps the seam's issue view onto the wire contract.
func toWireIssue(issue enhance.Issue) pkggeneric.Issue {
	out := pkggeneric.Issue{
		Number:        issue.Number,
		Title:         issue.Title,
		Body:          issue.Body,
		Labels:        issue.Labels,
		CloudResource: issue.CloudResource,
	}
	if issue.Repo != (source.Repo{}) {
		out.Repo = &pkggeneric.Repo{Owner: issue.Repo.Owner, Name: issue.Repo.Name}
	}
	return out
}

// toEnrichment maps the wire response onto the seam type.
func toEnrichment(resp *pkggeneric.EnhanceResponse) *enhance.Enrichment {
	return &enhance.Enrichment{
		Owners:          resp.Owners,
		CommentMarkdown: resp.CommentMarkdown,
		Attributes:      resp.Attributes,
		Repository:      resp.Repository,
	}
}
