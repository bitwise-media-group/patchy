// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/printer"
	"github.com/bitwise-media-group/patchy/internal/mirror"
)

// newMirrorSyncCmd is the publish path: converge the registries onto the
// committed tree.
func newMirrorSyncCmd(opts *Options, f *mirrorFlags) *cobra.Command {
	var (
		all        bool
		dryRun     bool
		registries []string
	)
	cmd := &cobra.Command{
		Use:   "sync [name]...",
		Short: "Publish committed charts and images to the mirror registries",
		Long: "Bring every configured registry in line with the committed state. The command\n" +
			"is idempotent. For each entry it re-pulls the upstream chart archive, and fails\n" +
			"loudly if upstream changed the released version (digest against the lock, tree\n" +
			"against the committed vendor/). Then, for each registry, it pushes the chart\n" +
			"unless its tag already exists (an existing tag is never replaced), copies every\n" +
			"locked image by digest, and signs everything it published that does not already\n" +
			"carry a valid mirror signature. It signs with that registry's signing block when\n" +
			"it has one, or else the global default.\n\n" +
			"Before a chart, image or artifact is first pushed or signed, sync checks its\n" +
			"upstream provenance against its verifyUpstream rule. Each one is checked once\n" +
			"per run however many registries it publishes to, and never when its copy is\n" +
			"already current and signed everywhere. So nothing reaches a registry unverified,\n" +
			"even when validate never ran. --dry-run reports what it would verify without\n" +
			"calling cosign.\n\n" +
			"--registry restricts publishing to the named registries (repeatable or\n" +
			"comma-separated). The default is all of them.\n\n" +
			"sync never writes to the working tree. Every write goes to a registry, so the\n" +
			"publish-on-merge pipeline creates no commits. Skips count as successes, so a\n" +
			"re-run after a partial failure finishes the rest.",
		Example: "  patchy mirror sync --all\n" +
			"  patchy mirror sync --all -o markdown       # publish summary, ready to paste\n" +
			"  patchy mirror sync --all --dry-run -o json\n" +
			"  patchy mirror sync --all --registry ghcr   # one registry only\n" +
			"  patchy mirror sync opentelemetry-collector",
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: mirrorEntryCompletion(f),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, loaded, err := mirrorRoot(cmd, opts, f)
			if err != nil {
				return err
			}
			eng, err := mirrorEngine(opts, f, root, loaded)
			if err != nil {
				return err
			}
			if _, err := eng.SelectRegistries(registries); err != nil {
				return errUsage(err)
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
				res, err := eng.Sync(cmd.Context(), entry, mirror.SyncOptions{DryRun: dryRun, Registries: registries})
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
	fl.StringSliceVar(&registries, "registry", nil,
		"restrict publishing to the named registries (repeatable or comma-separated; default all)")
	_ = cmd.RegisterFlagCompletionFunc("registry", registryCompletion)
	return cmd
}

// registryCompletion completes registry names from the loaded patchy
// config (mirror sync and build share it).
func registryCompletion(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	loaded, err := loadPatchyConfig()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var names []string
	for _, r := range loaded.File.Registries {
		names = append(names, r.Name)
	}
	return names, cobra.ShellCompDirectiveNoFileComp
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
			rows = append(rows, []string{r.Name, "", "", "failed: " + r.Err, ""})
			continue
		}
		for _, rec := range r.Records {
			signed := ""
			if rec.Signed {
				signed = "signed"
			}
			rows = append(rows, []string{r.Name, rec.Registry, rec.Ref, rec.Action, signed})
		}
	}
	return mirrorTable(opts, []string{"ENTRY", "REGISTRY", "REF", "ACTION", "SIGNED"}, rows)
}

// renderSyncMarkdown writes the sync outcome as a publish summary: one
// section per entry, one bullet per (artifact, registry), ready for a job
// summary without post-processing.
func renderSyncMarkdown(opts *Options, results []mirror.SyncResult) error {
	md := &markdownBuilder{}
	for _, r := range results {
		md.Section(r.Name)
		if r.Err != "" {
			md.Linef("**error:** %s", r.Err)
			continue
		}
		for _, rec := range r.Records {
			line := fmt.Sprintf("- `%s` [%s] %s", rec.Action, rec.Registry, rec.Ref)
			if rec.Signed {
				line += " (signed)"
			}
			md.Linef("%s", line)
		}
	}
	_, err := fmt.Fprint(opts.Out, md.String())
	return err
}
