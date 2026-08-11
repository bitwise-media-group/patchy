// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/printer"
	"github.com/bitwise-media-group/patchy/internal/mirror"
	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
)

// newMirrorUpgradeCmd is the update path: move pins forward and regenerate
// everything derived from them.
func newMirrorUpgradeCmd(opts *Options, f *mirrorFlags) *cobra.Command {
	var (
		all   bool
		group string
		to    string
		check bool
	)
	cmd := &cobra.Command{
		Use:   "upgrade [name]...",
		Short: "Move pins to newer upstream versions and regenerate derived state",
		Long: "Converge entries onto their target versions: resolve the newest upstream\n" +
			"version satisfying each entry's constraint, splice the pin, re-pick tracked\n" +
			"image tags past their cooldown, re-vendor, re-render, and regenerate the\n" +
			"digest locks. A fully-converged entry is a clean no-op.\n\n" +
			"Entries sharing a lockstep group bump together: the group target is the\n" +
			"lowest of the members' newest satisfying versions, holding the bump until\n" +
			"upstream has published every member's tag.\n\n" +
			"--check reports the plan as data without touching the tree (exit 0 with an\n" +
			"empty groups list when everything is current). patchy never commits: the\n" +
			"calling pipeline turns the mutated tree into branches and PRs.",
		Example: "  patchy mirror upgrade --check -o json\n" +
			"  patchy mirror upgrade --all\n" +
			"  patchy mirror upgrade --group flux --to 0.57.0\n" +
			"  patchy mirror upgrade opentelemetry-collector",
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
			entries, err := selectMirrorEntries(eng, args, all, group)
			if err != nil {
				return err
			}
			format, err := printer.ParseFormat(opts.Output)
			if err != nil {
				return errUsage(err)
			}

			if check {
				plan, err := eng.CheckUpdates(cmd.Context(), entries)
				if err != nil {
					return err
				}
				return renderUpdatePlan(opts, plan, format)
			}

			if to != "" && all {
				return errUsage(fmt.Errorf("--to needs named entries or a --group, not --all"))
			}
			targets, err := upgradeTargets(cmd, eng, entries, to)
			if err != nil {
				return err
			}
			results, failed := runUpgrades(cmd, eng, entries, targets, all)
			if err := renderUpgradeResults(opts, results, format); err != nil {
				return err
			}
			if failed > 0 {
				return fmt.Errorf("%d entr%s failed to upgrade", failed, plural(failed, "y", "ies"))
			}
			return nil
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&all, "all", false, "upgrade every entry in the store")
	fl.StringVar(&group, "group", "", "upgrade one lockstep group")
	fl.StringVar(&to, "to", "", "target version (default: newest satisfying the constraint)")
	fl.BoolVar(&check, "check", false, "report the update plan without touching the tree")
	_ = cmd.RegisterFlagCompletionFunc("group", noFileCompletion)
	_ = cmd.RegisterFlagCompletionFunc("to", noFileCompletion)
	return cmd
}

// upgradeTargets resolves each selected entry's target version: --to for
// every entry when set, otherwise the lockstep-aware plan's group targets
// (entries with no planned work keep their current pin).
func upgradeTargets(cmd *cobra.Command, eng *mirror.Engine, entries []spec.Entry,
	to string) (map[string]string, error) {
	targets := map[string]string{}
	if to != "" {
		for _, e := range entries {
			targets[e.Name] = to
		}
		return targets, nil
	}
	plan, err := eng.CheckUpdates(cmd.Context(), entries)
	if err != nil {
		return nil, err
	}
	for _, g := range plan.Groups {
		for _, m := range g.Members {
			targets[m.Name] = m.Target
		}
	}
	return targets, nil
}

// runUpgrades converges every entry, attempting all of them under --all
// and collecting per-entry failures instead of stopping.
func runUpgrades(cmd *cobra.Command, eng *mirror.Engine, entries []spec.Entry,
	targets map[string]string, all bool) ([]mirror.UpgradeResult, int) {
	var results []mirror.UpgradeResult
	failed := 0
	for _, entry := range entries {
		res, err := eng.Upgrade(cmd.Context(), entry, targets[entry.Name])
		if err != nil {
			failed++
			results = append(results, mirror.UpgradeResult{
				Name: entry.Name, Kind: entry.Kind,
				From: entry.Version(), To: entry.Version(),
				Err: err.Error(),
			})
			if !all {
				return results, failed
			}
			continue
		}
		results = append(results, *res)
	}
	return results, failed
}

// renderUpdatePlan writes the --check plan to stdout.
func renderUpdatePlan(opts *Options, plan *mirror.UpdatePlan, format printer.Format) error {
	switch format {
	case printer.FormatJSON:
		return mirrorJSON(opts, plan)
	case printer.FormatMarkdown:
		return renderUpdatePlanMarkdown(opts, plan)
	}
	if len(plan.Groups) == 0 {
		notef(opts.ErrOut, "patchy: everything is current\n")
		return nil
	}
	var rows [][]string
	for _, g := range plan.Groups {
		for _, m := range g.Members {
			rows = append(rows, []string{g.Group, m.Name, m.Current, m.Target, trackedSummary(m.TrackedImages)})
		}
	}
	return mirrorTable(opts, []string{"GROUP", "ENTRY", "CURRENT", "TARGET", "TRACKED"}, rows)
}

// renderUpdatePlanMarkdown writes the plan as an update summary: one
// section per group, one bullet per member.
func renderUpdatePlanMarkdown(opts *Options, plan *mirror.UpdatePlan) error {
	md := &markdownBuilder{}
	if len(plan.Groups) == 0 {
		md.Linef("Everything is current.")
	}
	for _, g := range plan.Groups {
		md.Section(g.Group)
		for _, m := range g.Members {
			line := fmt.Sprintf("- `%s` %s -> %s", m.Name, m.Current, m.Target)
			if tracked := trackedSummary(m.TrackedImages); tracked != "" {
				line += " (" + tracked + ")"
			}
			md.Linef("%s", line)
		}
	}
	_, err := fmt.Fprint(opts.Out, md.String())
	return err
}

// trackedSummary joins a member's moving tracked-image picks.
func trackedSummary(picks []mirror.TrackPlan) string {
	summary := ""
	for _, tp := range picks {
		if !tp.Changed() {
			continue
		}
		if summary != "" {
			summary += ", "
		}
		summary += fmt.Sprintf("%s %s->%s", tp.Image, tp.Current, tp.Selected)
	}
	return summary
}

// renderUpgradeResults writes the apply results to stdout.
func renderUpgradeResults(opts *Options, results []mirror.UpgradeResult, format printer.Format) error {
	switch format {
	case printer.FormatJSON:
		return mirrorJSON(opts, results)
	case printer.FormatMarkdown:
		return renderUpgradeResultsMarkdown(opts, results)
	}
	var rows [][]string
	for _, r := range results {
		rows = append(rows, []string{r.Name, strings.ToLower(r.Kind), upgradeVersion(r), upgradeStatus(r)})
	}
	return mirrorTable(opts, []string{"ENTRY", "KIND", "VERSION", "RESULT"}, rows)
}

// renderUpgradeResultsMarkdown writes the apply results as a summary: one
// section per entry, ready for a job summary without post-processing.
func renderUpgradeResultsMarkdown(opts *Options, results []mirror.UpgradeResult) error {
	md := &markdownBuilder{}
	for _, r := range results {
		md.Section(r.Name)
		if r.Err != "" {
			md.Linef("**error:** %s", r.Err)
			continue
		}
		md.Linef("- `%s` %s", upgradeVersion(r), upgradeStatus(r))
	}
	_, err := fmt.Fprint(opts.Out, md.String())
	return err
}

// upgradeStatus summarizes one result for the human formats.
func upgradeStatus(r mirror.UpgradeResult) string {
	switch {
	case r.Err != "":
		return "failed: " + r.Err
	case len(r.Changed) > 0:
		return strconv.Itoa(len(r.Changed)) + " file(s) changed"
	}
	return "unchanged"
}

// upgradeVersion renders a result's pin movement.
func upgradeVersion(r mirror.UpgradeResult) string {
	if r.To != r.From {
		return r.From + " -> " + r.To
	}
	return r.From
}

// plural picks a suffix by count.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// mirrorEntryCompletion completes entry names from the store.
func mirrorEntryCompletion(opts *Options,
	f *mirrorFlags) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		root, err := mirrorRoot(cmd, opts, f)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		entries, err := spec.Discover(root)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		var names []string
		for _, e := range entries {
			names = append(names, e.Name)
		}
		return names, cobra.ShellCompDirectiveNoFileComp
	}
}
