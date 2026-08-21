// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package cli

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/printer"
	"github.com/bitwise-media-group/patchy/internal/mirror"
	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
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
		Long: "Converge every configured registry onto the committed state, idempotently:\n" +
			"for each entry, re-pull the upstream chart archive and fail loudly if upstream\n" +
			"mutated the released version (digest vs lock, tree vs the committed vendor/),\n" +
			"then per registry push the chart unless its tag already exists (an existing\n" +
			"tag is never replaced), copy every locked image by digest, and sign everything\n" +
			"published that does not already carry a valid mirror signature — using that\n" +
			"registry's signing block when it has one, else the global default.\n\n" +
			"--registry restricts publishing to the named registries (repeatable or\n" +
			"comma-separated); the default is all of them.\n\n" +
			"sync is read-only on the working tree — every write goes to a registry —\n" +
			"so the publish-on-merge pipeline creates no commits. Skips are successes;\n" +
			"a re-run after a partial failure finishes the remainder.",
		Example: "  patchy mirror sync --all\n" +
			"  patchy mirror sync --all -o markdown       # publish summary, ready to paste\n" +
			"  patchy mirror sync --all --dry-run -o json\n" +
			"  patchy mirror sync --all --registry ghcr   # one registry only\n" +
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
	_ = cmd.RegisterFlagCompletionFunc("registry", mirrorRegistryCompletion(opts, f))
	return cmd
}

// mirrorRegistryCompletion completes registry names from mirror.yaml.
func mirrorRegistryCompletion(opts *Options,
	f *mirrorFlags) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		root, err := mirrorRoot(cmd, opts, f)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		global, err := spec.LoadConfig(filepath.Join(root, spec.ConfigFile))
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		var names []string
		for _, r := range global.Registries {
			names = append(names, r.Name)
		}
		return names, cobra.ShellCompDirectiveNoFileComp
	}
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
