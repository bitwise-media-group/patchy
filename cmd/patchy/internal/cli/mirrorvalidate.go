// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package cli

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/printer"
	"github.com/bitwise-media-group/patchy/internal/mirror"
)

// newMirrorValidateCmd is the CI gate: prove the committed tree is
// current, verified and clean, without touching it.
func newMirrorValidateCmd(opts *Options, f *mirrorFlags) *cobra.Command {
	var (
		all  bool
		only []string
	)
	cmd := &cobra.Command{
		Use:   "validate [name]...",
		Short: "Prove the committed store is current, verified and clean",
		Long: "Validate entries without touching the tree: regenerate the derived state\n" +
			"out-of-tree and byte-compare it with what is committed (a stale tree means\n" +
			"someone edited intent without running upgrade), verify upstream provenance,\n" +
			"scan the locked images with the enabled scanners, and lint the manifests\n" +
			"and CVE allowlists (statement + expiry within the policy horizon, rendered\n" +
			"output pulling only from the mirror unless allowed with a reason).\n\n" +
			"Wall-clock steps — tracked-tag picks, allowlist expiry stamping — never run\n" +
			"here, so the byte-identity gate stays deterministic between a commit and CI\n" +
			"validating it.",
		Example: "  patchy mirror validate --all\n" +
			"  patchy mirror validate --all -o markdown   # reviewer summary (PR comment, step summary)\n" +
			"  patchy mirror validate --all --only scan -o json\n" +
			"  patchy mirror validate opentelemetry-collector --only regen,lint",
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: mirrorEntryCompletion(opts, f),
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, stage := range only {
				if !slices.Contains(mirror.ValidateStages, stage) {
					return errUsage(fmt.Errorf("unknown stage %q (want %s)",
						stage, strings.Join(mirror.ValidateStages, ", ")))
				}
			}
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

			var results []mirror.ValidateResult
			failed := 0
			for _, entry := range entries {
				res, err := eng.Validate(cmd.Context(), entry, only)
				if err != nil {
					failed++
					results = append(results, mirror.ValidateResult{
						Name: entry.Name, Kind: entry.Kind, Err: err.Error(),
					})
					if !all {
						break
					}
					continue
				}
				if res.Failed() {
					failed++
				}
				results = append(results, *res)
			}
			if err := renderValidateResults(opts, results, format); err != nil {
				return err
			}
			if failed > 0 {
				return fmt.Errorf("%d entr%s failed validation", failed, plural(failed, "y", "ies"))
			}
			return nil
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&all, "all", false, "validate every entry in the store")
	fl.StringSliceVar(&only, "only", nil,
		"run only these stages: "+strings.Join(mirror.ValidateStages, ", "))
	_ = cmd.RegisterFlagCompletionFunc("only", fixedCompletion(mirror.ValidateStages))
	return cmd
}

// renderValidateResults writes the validation outcome to stdout.
func renderValidateResults(opts *Options, results []mirror.ValidateResult, format printer.Format) error {
	switch format {
	case printer.FormatJSON:
		return mirrorJSON(opts, results)
	case printer.FormatMarkdown:
		return renderValidateMarkdown(opts, results)
	}
	var rows [][]string
	for _, r := range results {
		var problems []string
		if r.Err != "" {
			problems = append(problems, r.Err)
		}
		if len(r.RegenDiffs) > 0 {
			problems = append(problems, strconv.Itoa(len(r.RegenDiffs))+" stale derived file(s)")
		}
		if len(r.Lint) > 0 {
			problems = append(problems, strconv.Itoa(len(r.Lint))+" lint issue(s)")
		}
		if r.Scan != nil && r.Scan.Failed() {
			problems = append(problems, strconv.Itoa(len(r.Scan.Blocking))+" blocking finding(s)")
		}
		status := "ok"
		if len(problems) > 0 {
			status = strings.Join(problems, "; ")
		}
		rows = append(rows, []string{r.Name, strings.ToLower(r.Kind), status})
	}
	return mirrorTable(opts, []string{"ENTRY", "KIND", "RESULT"}, rows)
}

// renderValidateMarkdown writes the outcome as a reviewer-facing summary:
// one section per entry, problems as fenced blocks, ready to paste into a
// PR comment or step summary without post-processing.
func renderValidateMarkdown(opts *Options, results []mirror.ValidateResult) error {
	md := &markdownBuilder{}
	for _, r := range results {
		md.Section(r.Name)
		clean := true
		if r.Err != "" {
			md.Linef("**error:** %s", r.Err)
			clean = false
		}
		if len(r.RegenDiffs) > 0 {
			md.Fenced("**stale derived state** (run upgrade):", r.RegenDiffs)
			clean = false
		}
		if len(r.Lint) > 0 {
			md.Fenced("**lint:**", r.Lint)
			clean = false
		}
		if r.Scan != nil {
			var findings []string
			for _, f := range r.Scan.Blocking {
				line := fmt.Sprintf("%s %s — %s %s", f.ID, f.Severity, f.Package, f.Installed)
				if len(f.FixedIn) > 0 {
					line += " (fixed in " + strings.Join(f.FixedIn, ", ") + ")"
				}
				findings = append(findings, line)
			}
			if len(findings) > 0 {
				md.Fenced("**blocking findings:**", findings)
				clean = false
			}
			if r.Scan.ConfigScanFailed {
				md.Linef("**configuration scan failed**")
				clean = false
			}
			if r.Scan.Suppressed > 0 {
				md.Linef("%d finding(s) suppressed by the allowlist.", r.Scan.Suppressed)
			}
		}
		if r.Verify != nil && len(r.Verify.Gaps) > 0 {
			md.Linef("Provenance gaps (documented): %s", strings.Join(r.Verify.Gaps, "; "))
		}
		if clean {
			md.Linef("clean ✔")
		}
	}
	_, err := fmt.Fprint(opts.Out, md.String())
	return err
}
