// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package cli

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/devharness"
	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/printer"
	"github.com/bitwise-media-group/patchy/pkg/source"
)

// newDevCmd groups the local test harnesses for generic-integration authors.
// Everything under `dev` runs on the workstation and never touches a
// cluster; the persistent kubeconfig flags are inert here, as they are for
// `completion`.
func newDevCmd(opts *Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dev",
		Short: "Local test harnesses for generic-integration authors",
		Long: "Test a generic integration against the real patchy contract without a cluster:\n" +
			"`dev generic` hosts the receiver locally and drives the enhancer and resolver\n" +
			"calls; `dev enhance` and `dev resolve` fire a single outbound exchange for\n" +
			"authors whose process is only an enhancer or resolver.\n\n" +
			"Every flag also resolves from a PATCHY_DEV_* environment variable and an\n" +
			"optional .patchy.yaml (or .yml/.json) in the working directory, with explicit\n" +
			"flags winning over the environment, and the environment over the file.",
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(newDevGenericCmd(opts), newDevEnhanceCmd(opts), newDevResolveCmd(opts))
	return cmd
}

// devEmitter builds the event sink for a dev command: NDJSON on stdout under
// -o json (one Event per line, pipeable), otherwise human rendering on
// stderr — a long-running harness narrates, and nothing must lie to a pipe.
func devEmitter(opts *Options) (func(devharness.Event), error) {
	format, err := printer.ParseFormat(opts.Output)
	if err != nil {
		return nil, errUsage(err)
	}
	if format == printer.FormatJSON {
		enc := json.NewEncoder(opts.Out)
		return func(e devharness.Event) { _ = enc.Encode(e) }, nil
	}
	p := printer.New(opts.ErrOut, format, printer.Color(opts.ErrOut, opts.NoColor))
	return func(e devharness.Event) { renderDevEvent(opts, p, e) }, nil
}

// renderDevEvent renders one harness event for a human: pipeline milestones
// as documents, transport chatter as narration lines.
func renderDevEvent(opts *Options, p *printer.Printer, e devharness.Event) {
	switch e.Kind {
	case "listening":
		notef(opts.ErrOut, "patchy: listening — POST signed findings payloads to %s\n", e.WebhookURL)
	case "delivery":
		notef(opts.ErrOut, "patchy: delivery %s: %d finding(s) ingested\n", e.DeliveryID, e.Ingested)
		if e.Note != "" {
			notef(opts.ErrOut, "patchy: note: %s\n", e.Note)
		}
	case "finding":
		doc := p.Doc().Section("Finding "+findingID(e.Finding)).
			Field("Severity", e.Finding.Severity).
			Field("Title", e.Finding.Title).
			Field("Rule", e.Finding.RuleID).
			Field("Advisories", strings.Join(e.Finding.Advisories, ", ")).
			Field("URL", e.Finding.HTMLURL)
		if e.Finding.Repo != (source.Repo{}) {
			doc.Field("Repository", e.Finding.Repo.String())
		}
		if cr := e.Finding.CloudResource; cr != nil {
			doc.Fieldf("Cloud resource", "%s %s (%s)", cr.Provider, cr.Name, cr.Type)
		}
		_ = doc.Render()
	case "enhance":
		doc := p.Doc().Section("Enhance")
		if resp := e.EnhanceResponse; resp != nil {
			doc.Field("Owners", strings.Join(resp.Owners, ", "))
			doc.Field("Attributes", joinAttributes(resp.Attributes))
			if resp.Repository != nil {
				doc.Fieldf("Repository", "%s %s/%s", resp.Repository.Provider, resp.Repository.Owner, resp.Repository.Name)
			}
			doc.Body(resp.CommentMarkdown)
		}
		doc.Field("Error", e.Err)
		doc.Field("Note", e.Note)
		_ = doc.Render()
	case "resolve":
		doc := p.Doc().Section("Resolve")
		if req := e.ResolveRequest; req != nil {
			for _, a := range req.Alerts {
				doc.Field("Alert", a.ID)
			}
			doc.Fieldf("Verdict", "%s (%s)", req.Verdict.Kind, req.Verdict.Reason)
		}
		doc.Field("Error", e.Err)
		doc.Field("Note", e.Note)
		_ = doc.Render()
	case "error":
		notef(opts.ErrOut, "patchy: delivery %s rejected: %s\n", e.DeliveryID, e.Err)
		if e.Note != "" {
			notef(opts.ErrOut, "patchy: note: %s\n", e.Note)
		}
	}
}

// findingID names a finding the way its source system does.
func findingID(f *source.Finding) string {
	if f.AlertID != "" {
		return f.AlertID
	}
	return fmt.Sprintf("#%d", f.AlertNumber)
}

// joinAttributes flattens an attribute map for a field line; sorted so
// transcripts stay diffable.
func joinAttributes(attrs map[string]string) string {
	parts := make([]string, 0, len(attrs))
	for k, v := range attrs {
		parts = append(parts, k+"="+v)
	}
	slices.Sort(parts)
	return strings.Join(parts, ", ")
}
