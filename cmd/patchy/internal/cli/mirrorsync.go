// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/printer"
	"github.com/bitwise-media-group/patchy/internal/mirror"
)

// newMirrorSyncCmd is the publish path: converge the registry onto the
// committed tree.
func newMirrorSyncCmd(opts *Options, f *mirrorFlags) *cobra.Command {
	var (
		all    bool
		dryRun bool
	)
	cmd := &cobra.Command{
		Use:   "sync [name]...",
		Short: "Publish committed charts and images to the mirror registry",
		Long: "Converge the registry onto the committed state, idempotently: for each\n" +
			"entry, re-pull the upstream chart archive and fail loudly if upstream mutated\n" +
			"the released version (digest vs lock, tree vs the committed vendor/), push\n" +
			"the chart unless its tag already exists (an existing tag is never replaced),\n" +
			"copy every locked image by digest, and sign everything published that does\n" +
			"not already carry a valid mirror signature.\n\n" +
			"sync is read-only on the working tree — every write goes to the registry —\n" +
			"so the publish-on-merge pipeline creates no commits. Skips are successes;\n" +
			"a re-run after a partial failure finishes the remainder.",
		Example: "  patchy mirror sync --all\n" +
			"  patchy mirror sync --all -o markdown       # publish summary, ready to paste\n" +
			"  patchy mirror sync --all --dry-run -o json\n" +
			"  patchy mirror sync opentelemetry-collector",
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: mirrorEntryCompletion(opts, f),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := mirrorRoot(cmd, opts, f)
			if err != nil {
				return err
			}
			eng, err := mirrorEngine(opts, f, root)
			if err != nil {
				return err
			}
			entries, err := selectMirrorEntries(eng, args, all, "")
			if err != nil {
				return err
			}
			format, err := printer.ParseFormat(opts.Output)
			if err != nil {
				return errUsage(err)
			}

			var results []mirror.SyncResult
			failed := 0
			for _, entry := range entries {
				res, err := eng.Sync(cmd.Context(), entry, dryRun)
				if err != nil {
					failed++
					results = append(results, mirror.SyncResult{Name: entry.Name, Kind: entry.Kind, Err: err.Error()})
					if !all {
						break
					}
					continue
				}
				results = append(results, *res)
			}
			if err := renderSyncResults(opts, results, format); err != nil {
				return err
			}
			if failed > 0 {
				return fmt.Errorf("%d entr%s failed to sync", failed, plural(failed, "y", "ies"))
			}
			return nil
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&all, "all", false, "sync every entry in the store")
	fl.BoolVar(&dryRun, "dry-run", false, "report every action without writing to the registry")
	return cmd
}

// renderSyncResults writes the sync outcome to stdout.
func renderSyncResults(opts *Options, results []mirror.SyncResult, format printer.Format) error {
	switch format {
	case printer.FormatJSON:
		return mirrorJSON(opts, results)
	case printer.FormatMarkdown:
		return renderSyncMarkdown(opts, results)
	}
	var rows [][]string
	for _, r := range results {
		if r.Err != "" {
			rows = append(rows, []string{r.Name, "", "failed: " + r.Err, ""})
			continue
		}
		for _, rec := range r.Records {
			signed := ""
			if rec.Signed {
				signed = "signed"
			}
			rows = append(rows, []string{r.Name, rec.Ref, rec.Action, signed})
		}
	}
	return mirrorTable(opts, []string{"ENTRY", "REF", "ACTION", "SIGNED"}, rows)
}

// renderSyncMarkdown writes the sync outcome as a publish summary: one
// section per entry, one bullet per artifact, ready for a job summary
// without post-processing.
func renderSyncMarkdown(opts *Options, results []mirror.SyncResult) error {
	md := &markdownBuilder{}
	for _, r := range results {
		md.Section(r.Name)
		if r.Err != "" {
			md.Linef("**error:** %s", r.Err)
			continue
		}
		for _, rec := range r.Records {
			line := fmt.Sprintf("- `%s` %s", rec.Action, rec.Ref)
			if rec.Signed {
				line += " (signed)"
			}
			md.Linef("%s", line)
		}
	}
	_, err := fmt.Fprint(opts.Out, md.String())
	return err
}
