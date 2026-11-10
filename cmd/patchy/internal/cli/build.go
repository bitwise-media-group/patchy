// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package cli

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/printer"
	"github.com/bitwise-media-group/patchy/internal/build"
)

// buildEngineHook adjusts the engine config before the engine is built; a
// seam for tests to inject fakes (builder, scanners, signer). nil in
// production.
var buildEngineHook func(*build.Config)

// newBuildCmd builds, scans, publishes and signs first-party artifacts.
// Cluster-free like mirror: the persistent kubeconfig flags are inert here.
func newBuildCmd(opts *Options) *cobra.Command {
	var (
		all              bool
		dryRun           bool
		updateAllowlists bool
		registries       []string
		tags             []string
	)
	cmd := &cobra.Command{
		Use:   "build [name]...",
		Short: "Build, scan, publish and sign first-party images and Flux manifests",
		Long: "Build the artifacts the patchy config's build: block lists — Dockerfile images\n" +
			"(build.images) and Flux manifest directories (build.manifests) — scan them\n" +
			"before anything is pushed, publish them to every configured registry as\n" +
			"<registry.url>/<repository>:<tag>, and sign them with that registry's signing\n" +
			"block (else the global one). It is the first-party sibling of `patchy mirror`\n" +
			"and shares its registries, signing and scan blocks.\n\n" +
			"Images build with `docker buildx` into an OCI layout; every platform image is\n" +
			"scanned (osv-scanner and/or grype) under the entry's resolved scan policy and\n" +
			"its allowlist, security/allowlists/<name>.yaml. Any blocking finding the\n" +
			"allowlist does not accept fails the entry with nothing pushed; a lapsed\n" +
			"allowlist entry is reported and accepts nothing. Manifests directories are\n" +
			"staged (ignore patterns applied, symlinks refused), swept by kubescape, and\n" +
			"published as-is as a Flux OCI artifact.\n\n" +
			"Tags come from ${ENV} templates: ${VAR}, ${VAR:-default}, ${VAR:offset:length}\n" +
			"and $$, plus the built-in ${PATCHY_TIMESTAMP} (UTC yyyymmddhhmmss, fixed once per\n" +
			"run; from SOURCE_DATE_EPOCH when set, overridable by a real PATCHY_TIMESTAMP).\n" +
			"Tags never move: an absent tag is pushed, one already at the build's digest is\n" +
			"skipped, and one at another digest is left alone with a warning. With\n" +
			"SOURCE_DATE_EPOCH set, rebuilds of one commit reproduce the same digest.\n\n" +
			"--dry-run builds and scans but never contacts a registry (safe in PR CI with\n" +
			"no push credentials), and needs no registries configured: without any, it\n" +
			"reports each build's digest. --update-allowlists rebuilds and rescans each image\n" +
			"entry whose scan.allowlist.generate is true and regenerates its allowlist —\n" +
			"never pushing, signing, or failing on findings: the PR review of the diff is\n" +
			"where the risk is accepted.\n\n" +
			"Needs docker with buildx (multi-platform builds need a docker-container\n" +
			"builder or the containerd image store), cosign (>= v3) for signing, and the\n" +
			"enabled scanners on PATH.\n\n" + configHelp,
		Example: "  patchy build --all --dry-run                      # PR CI: build and scan only\n" +
			"  patchy build --all -o markdown                    # publish, summary ready to paste\n" +
			"  patchy build controller --registry ghcr\n" +
			"  patchy build --all --tag 'v${VERSION}'            # replace the configured tags\n" +
			"  patchy build --all --update-allowlists            # regenerate security/allowlists/",
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: buildEntryCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			loaded, err := loadPatchyConfig()
			if err != nil {
				return err
			}
			noteConfigSources(opts, loaded)
			resolve := loaded.File.BuildConfig
			if dryRun {
				// A plan never contacts a registry, so it needs none
				// configured: CI and workstations build and scan with no
				// registry URLs anywhere.
				resolve = loaded.File.BuildPlanConfig
			}
			global, spec, err := resolve()
			if err != nil {
				return err
			}
			if len(global.Registries) == 0 {
				notef(opts.ErrOut, "patchy: no registries configured; reporting each build's digest only\n")
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
				// --all over an empty build: block. Nothing to do is a
				// success, so a shared task (`mise run build`) can run it in
				// a repo that only mirrors.
				notef(opts.ErrOut, "patchy: the patchy config has no build entries "+
					"(build.images, build.manifests); nothing to build\n")
			}
			if updateAllowlists {
				return runUpdateAllowlists(cmd, opts, eng, names, all, format)
			}

			results := []build.Result{}
			failed := 0
			for _, name := range names {
				res, err := eng.Build(cmd.Context(), name, build.Options{
					DryRun: dryRun, Registries: registries, Tags: tags,
				})
				if res == nil {
					res = &build.Result{Name: name}
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
			if err := renderBuildResults(opts, results, format); err != nil {
				return err
			}
			if failed > 0 {
				return fmt.Errorf("%d entr%s failed to build", failed, plural(failed, "y", "ies"))
			}
			return nil
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&all, "all", false,
		"build every configured entry, continuing past failures (none configured is a no-op)")
	fl.BoolVar(&dryRun, "dry-run", false, "build and scan, but never contact a registry")
	fl.BoolVar(&updateAllowlists, "update-allowlists", false,
		"rebuild, rescan and regenerate security/allowlists/<name>.yaml; never publishes")
	fl.StringSliceVar(&registries, "registry", nil,
		"restrict publishing to the named registries (repeatable or comma-separated; default all)")
	fl.StringSliceVar(&tags, "tag", nil,
		"replace the configured tag templates for this run (repeatable; still ${ENV}-expanded)")
	cmd.MarkFlagsMutuallyExclusive("dry-run", "update-allowlists")
	cmd.AddCommand(newBuildPromoteCmd(opts))
	_ = cmd.RegisterFlagCompletionFunc("registry", registryCompletion)
	_ = cmd.RegisterFlagCompletionFunc("tag", noFileCompletion)
	return cmd
}

// selectBuildEntries resolves the entries a run covers: explicit names (in
// the order given) or every entry.
func selectBuildEntries(eng *build.Engine, names []string, all bool) ([]string, error) {
	switch {
	case all && len(names) > 0:
		return nil, errUsage(errors.New("--all cannot be combined with entry names"))
	case all:
		// Every entry, which may be none: the caller narrates an empty run.
		var out []string
		for _, e := range eng.Entries() {
			out = append(out, e.Name)
		}
		return out, nil
	case len(names) > 0:
		for _, n := range names {
			if _, err := eng.Entry(n); err != nil {
				return nil, errUsage(err)
			}
		}
		return names, nil
	default:
		return nil, errUsage(errors.New("name one or more build entries, or pass --all"))
	}
}

// runUpdateAllowlists regenerates each selected entry's allowlist.
func runUpdateAllowlists(cmd *cobra.Command, opts *Options, eng *build.Engine, names []string, all bool,
	format printer.Format) error {
	results := []build.AllowlistResult{}
	failed := 0
	for _, name := range names {
		res, err := eng.UpdateAllowlist(cmd.Context(), name)
		if res == nil {
			res = &build.AllowlistResult{Name: name}
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
	if err := renderAllowlistResults(opts, results, format); err != nil {
		return err
	}
	if failed > 0 {
		return fmt.Errorf("%d allowlist update%s failed", failed, plural(failed, "", "s"))
	}
	return nil
}

// buildEntryCompletion completes entry names from the loaded config.
func buildEntryCompletion(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	loaded, err := loadPatchyConfig()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var names []string
	for _, e := range loaded.File.Build.Entries() {
		names = append(names, e.Name)
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}

// renderBuildResults writes the publish outcome to stdout.
func renderBuildResults(opts *Options, results []build.Result, format printer.Format) error {
	switch format {
	case printer.FormatJSON:
		return mirrorJSON(opts, results)
	case printer.FormatMarkdown:
		return renderBuildMarkdown(opts, results)
	}
	var rows [][]string
	for _, r := range results {
		if r.Err != "" {
			rows = append(rows, []string{r.Name, "", "", "failed: " + r.Err, ""})
			continue
		}
		if len(r.Records) == 0 && r.Digest != "" {
			// a dry run with no registries configured: the build itself
			rows = append(rows, []string{r.Name, "", r.Digest, "built", ""})
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

// renderBuildMarkdown writes the outcome as a publish summary: one section
// per entry with its digest, scan line and one bullet per (registry, tag).
func renderBuildMarkdown(opts *Options, results []build.Result) error {
	md := &markdownBuilder{}
	for _, r := range results {
		md.Section(r.Name)
		if r.Digest != "" {
			md.Linef("`%s` %s", r.Kind, r.Digest)
			md.Linef("")
		}
		if line := scanSummary(r.Scan); line != "" {
			md.Linef("%s", line)
			md.Linef("")
		}
		if r.Err != "" {
			md.Linef("**error:** %s", r.Err)
			if r.Scan != nil && len(r.Scan.Blocking) > 0 {
				items := make([]string, 0, len(r.Scan.Blocking))
				for _, f := range r.Scan.Blocking {
					items = append(items, fmt.Sprintf("%s %s %s %s (fixed in %s)",
						f.ID, f.Severity, f.Package, f.Installed, strings.Join(f.FixedIn, ", ")))
				}
				md.Linef("")
				md.Fenced("Blocking findings:", items)
			}
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

// scanSummary renders one result's scan report as a sentence.
func scanSummary(s *build.ScanReport) string {
	switch {
	case s == nil:
		return ""
	case s.Skipped != "":
		return "Scan: " + s.Skipped + "."
	case s.ConfigScan != "":
		return "Configuration scan: " + s.ConfigScan + "."
	case len(s.Scanned) == 0:
		return ""
	}
	line := fmt.Sprintf("Scanned %s: %d blocking, %d accepted by the allowlist.",
		strings.Join(s.Scanned, ", "), len(s.Blocking), s.Suppressed)
	if len(s.Expired) > 0 {
		line += " Lapsed allowlist entries: " + strings.Join(s.Expired, ", ") + "."
	}
	return line
}

// renderAllowlistResults writes the --update-allowlists outcome.
func renderAllowlistResults(opts *Options, results []build.AllowlistResult, format printer.Format) error {
	switch format {
	case printer.FormatJSON:
		return mirrorJSON(opts, results)
	case printer.FormatMarkdown:
		md := &markdownBuilder{}
		for _, r := range results {
			md.Section(r.Name)
			md.Linef("- %s", allowlistStatus(r))
		}
		_, err := fmt.Fprint(opts.Out, md.String())
		return err
	}
	var rows [][]string
	for _, r := range results {
		rows = append(rows, []string{r.Name, r.Path, allowlistStatus(r)})
	}
	return mirrorTable(opts, []string{"ENTRY", "ALLOWLIST", "RESULT"}, rows)
}

// allowlistStatus summarizes one regeneration.
func allowlistStatus(r build.AllowlistResult) string {
	switch {
	case r.Err != "":
		return "failed: " + r.Err
	case !r.Generated:
		return "skipped: " + r.Note
	}
	return strconv.Itoa(r.Kept) + " kept, " + strconv.Itoa(len(r.Renewed)) + " renewed, " +
		strconv.Itoa(r.Added) + " new, " + strconv.Itoa(len(r.Dropped)) + " dropped"
}
