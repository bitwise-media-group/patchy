// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/printer"
	"github.com/bitwise-media-group/patchy/internal/build"
)

// newBuildPromoteCmd adds a tag to already-published builds instead of
// rebuilding them.
func newBuildPromoteCmd(opts *Options) *cobra.Command {
	var (
		all        bool
		dryRun     bool
		from       string
		tag        string
		registries []string
	)
	cmd := &cobra.Command{
		Use:   "promote [name]... --from <pattern> --tag <tag>",
		Short: "Promote published builds by adding a tag to them, without rebuilding",
		Long: "Promote an already-published build of each entry by adding a tag to its digest:\n" +
			"what was tested is exactly what ships. In every configured registry, the newest\n" +
			"tag of <registry.url>/<repository> matching --from (a glob; semver order, so\n" +
			"X.Y.Z-edge.<yyyymmddhhmmss>.g<commit> builds order by their timestamp) is the\n" +
			"build promoted. Its digest must carry a valid signature under that registry's\n" +
			"signing config, and --tag is then added to it.\n\n" +
			"Tags never move: a --tag already on that digest is skipped, and one on another\n" +
			"digest fails the entry. Nothing is built, scanned or signed — the build's own\n" +
			"signature is the one verified. --from and --tag are ${ENV}-expanded like the\n" +
			"build's tag templates.\n\n" +
			"Each row reports the build's org.opencontainers.image.revision annotation (every\n" +
			"manifests artifact carries one), so a release pipeline can check the build came\n" +
			"from the commit it is releasing. --dry-run resolves and verifies, and tags\n" +
			"nothing.\n\n" + configHelp,
		Example: "  patchy build promote --all --from '${VERSION}-edge.*' --tag 'v${VERSION}'\n" +
			"  patchy build promote platform --from '1.4.0-edge.*' --tag v1.4.0 --dry-run -o json",
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: buildEntryCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			if from == "" || tag == "" {
				return errUsage(errors.New("--from and --tag are required"))
			}
			loaded, err := loadPatchyConfig()
			if err != nil {
				return err
			}
			noteConfigSources(opts, loaded)
			global, spec, err := loaded.File.BuildConfig()
			if err != nil {
				return err
			}
			format, err := printer.ParseFormat(opts.Output)
			if err != nil {
				return errUsage(err)
			}
			cfg := build.Config{
				Workdir: loaded.Workdir,
				Global:  global,
				Spec:    spec,
				OnEvent: func(e build.Event) { renderEvent(opts, e.Kind, e.Entry, e.Message) },
			}
			if buildEngineHook != nil {
				buildEngineHook(&cfg)
			}
			eng, err := build.New(cfg)
			if err != nil {
				return err
			}
			if _, err := eng.SelectRegistries(registries); err != nil {
				return errUsage(err)
			}
			names, err := selectBuildEntries(eng, args, all)
			if err != nil {
				return err
			}
			if len(names) == 0 {
				notef(opts.ErrOut, "patchy: the patchy config has no build entries "+
					"(build.images, build.manifests); nothing to promote\n")
			}
			results := []build.PromoteResult{}
			failed := 0
			for _, name := range names {
				res, err := eng.Promote(cmd.Context(), name, build.PromoteOptions{
					From: from, Tag: tag, Registries: registries, DryRun: dryRun,
				})
				if res == nil {
					res = &build.PromoteResult{Name: name}
				}
				if err != nil {
					failed++
					res.Err = err.Error()
				}
				results = append(results, *res)
				if err != nil && !all {
					break
				}
			}
			if err := renderPromoteResults(opts, results, format); err != nil {
				return err
			}
			if failed > 0 {
				return fmt.Errorf("%d entr%s failed to promote", failed, plural(failed, "y", "ies"))
			}
			return nil
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&all, "all", false,
		"promote every configured entry, continuing past failures (none configured is a no-op)")
	fl.BoolVar(&dryRun, "dry-run", false, "resolve and verify the builds to promote, but tag nothing")
	fl.StringVar(&from, "from", "",
		"glob over the repository's tags selecting the builds to promote from; the newest wins (${ENV}-expanded)")
	fl.StringVar(&tag, "tag", "", "the tag to add to the promoted build (${ENV}-expanded)")
	fl.StringSliceVar(&registries, "registry", nil,
		"restrict promotion to the named registries (repeatable or comma-separated; default all)")
	_ = cmd.RegisterFlagCompletionFunc("registry", registryCompletion)
	_ = cmd.RegisterFlagCompletionFunc("from", noFileCompletion)
	_ = cmd.RegisterFlagCompletionFunc("tag", noFileCompletion)
	return cmd
}

// renderPromoteResults writes the promotion outcome to stdout.
func renderPromoteResults(opts *Options, results []build.PromoteResult, format printer.Format) error {
	switch format {
	case printer.FormatJSON:
		return mirrorJSON(opts, results)
	case printer.FormatMarkdown:
		md := &markdownBuilder{}
		for _, r := range results {
			md.Section(r.Name)
			if r.Err != "" {
				md.Linef("**error:** %s", r.Err)
				continue
			}
			for _, rec := range r.Records {
				line := fmt.Sprintf("- `%s` [%s] %s ← %s (%s)", rec.Action, rec.Registry, rec.Ref, rec.Source, rec.Digest)
				if rec.Revision != "" {
					line += ", revision " + rec.Revision
				}
				md.Linef("%s", line)
			}
		}
		_, err := fmt.Fprint(opts.Out, md.String())
		return err
	}
	var rows [][]string
	for _, r := range results {
		if r.Err != "" {
			rows = append(rows, []string{r.Name, "", "", "", "failed: " + r.Err})
			continue
		}
		for _, rec := range r.Records {
			rows = append(rows, []string{r.Name, rec.Registry, rec.Ref, rec.Source, rec.Action})
		}
	}
	return mirrorTable(opts, []string{"ENTRY", "REGISTRY", "REF", "FROM", "ACTION"}, rows)
}
