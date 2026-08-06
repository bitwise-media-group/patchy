// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package devharness

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/bitwise-media-group/patchy/internal/generic"
	pkggeneric "github.com/bitwise-media-group/patchy/pkg/generic"
	"github.com/bitwise-media-group/patchy/pkg/source"
)

// CanonicalVerdict is the verdict write-back patchy sends today, verbatim
// from the integration-controller's resolveAlerts: dismissal after an
// investigation recommended ignore is the only path that resolves.
var CanonicalVerdict = source.Verdict{
	Kind:    source.VerdictIgnore,
	Reason:  "false positive",
	Comment: "Dismissed by patchy: investigation recommended ignore.",
}

// ParseFindings validates one findings payload — the same wire envelope the
// webhook receives, so fixture files work in both places — through the real
// source handler, under the given integration name.
func ParseFindings(ctx context.Context, name string, payload []byte) ([]source.Finding, error) {
	event, err := generic.Detect(payload)
	if err != nil {
		return nil, err
	}
	if event != generic.EventFindings {
		return nil, fmt.Errorf("payload is a %q event; it carries no findings", event)
	}
	return generic.NewSource(name, generic.Options{}).Findings(ctx, event, payload)
}

// Enhance sends one enhancement request per finding, emitting an event per
// call, and returns the joined failures.
func Enhance(ctx context.Context, c *generic.Client, name string, findings []source.Finding, emit func(Event)) error {
	var errs []error
	for _, f := range findings {
		ev := enhanceOne(ctx, c, name, f)
		emit(ev)
		if ev.Err != "" {
			errs = append(errs, errors.New(ev.Err))
		}
	}
	return errors.Join(errs...)
}

// Resolve sends one verdict write-back per finding, emitting an event per
// call, and returns the joined failures.
func Resolve(ctx context.Context, c *generic.Client, name string, findings []source.Finding, emit func(Event)) error {
	var errs []error
	for _, f := range findings {
		ev := resolveOne(ctx, c, name, f)
		emit(ev)
		if ev.Err != "" {
			errs = append(errs, errors.New(ev.Err))
		}
	}
	return errors.Join(errs...)
}

// issueFor is the view of a finding an enhancer receives. It mirrors what
// the context controller sends for a fresh finding: enhanceInput
// (internal/controller/context) sets only Title, Body (the description),
// Repo and CloudResource — Number exists only once a tracking issue does,
// and Labels are never populated — and toWireIssue (internal/enhancers) maps
// that onto the wire.
func issueFor(f source.Finding) pkggeneric.Issue {
	issue := pkggeneric.Issue{
		Title:         f.Title,
		Body:          f.Description,
		CloudResource: f.CloudResource,
	}
	if f.Repo != (source.Repo{}) {
		issue.Repo = &pkggeneric.Repo{Owner: f.Repo.Owner, Name: f.Repo.Name}
	}
	return issue
}

// alertRef identifies a finding for write-back: the delivered alertId, else
// the alert number as a string — the same rule the controller's toAlertRefs
// applies.
func alertRef(name string, f source.Finding) source.AlertRef {
	id := f.AlertID
	if id == "" {
		id = strconv.Itoa(f.AlertNumber)
	}
	return source.AlertRef{ID: id, Source: name, URL: f.HTMLURL}
}

// enhanceOne performs one enhancer exchange and describes it as an event.
func enhanceOne(ctx context.Context, c *generic.Client, name string, f source.Finding) Event {
	req := pkggeneric.EnhanceRequest{Version: pkggeneric.Version, Integration: name, Issue: issueFor(f)}
	ev := Event{Kind: "enhance", EnhanceRequest: &req}
	resp, err := c.Enhance(ctx, req)
	switch {
	case err != nil:
		ev.Err = err.Error()
		ev.Note = "production logs this and moves on (warn-and-skip); a repo-less cloud finding would hold and retry"
	case resp == nil:
		ev.Note = "204 or empty body — nothing to contribute; the finding proceeds unenhanced"
	default:
		ev.EnhanceResponse = resp
		if len(resp.CommentMarkdown) > maxEnrichmentMarkdown {
			ev.Note = fmt.Sprintf("commentMarkdown is %d bytes; production truncates it at %d",
				len(resp.CommentMarkdown), maxEnrichmentMarkdown)
		}
	}
	return ev
}

// resolveOne performs one verdict write-back and describes it as an event.
func resolveOne(ctx context.Context, c *generic.Client, name string, f source.Finding) Event {
	ref := alertRef(name, f)
	ev := Event{
		Kind: "resolve",
		ResolveRequest: &pkggeneric.ResolveRequest{
			Version:     pkggeneric.Version,
			Integration: name,
			Alerts:      []pkggeneric.AlertRef{{ID: ref.ID, URL: ref.URL}},
			Verdict: pkggeneric.Verdict{
				Kind:    string(CanonicalVerdict.Kind),
				Reason:  CanonicalVerdict.Reason,
				Comment: CanonicalVerdict.Comment,
			},
		},
		Note: "any 2xx is success and patchy retries failures, so your endpoint must be idempotent; " +
			"production sends this once per finding at dismissal, carrying every accumulated alert",
	}
	if err := generic.NewResolver(c, name).Resolve(ctx, []source.AlertRef{ref}, CanonicalVerdict); err != nil {
		ev.Err = err.Error()
	}
	return ev
}
