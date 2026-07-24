// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/browser"
	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/kubecfg"
	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/printer"
	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/render"
	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/resource"
)

// reviewFlags are the options `review` accepts.
type reviewFlags struct {
	finding  string
	attempt  int32
	raw      bool
	web      bool
	printURL bool
}

func newReviewCmd(opts *Options) *cobra.Command {
	f := &reviewFlags{}
	cmd := &cobra.Command{
		Use:   "review <resource> [name]",
		Short: "Read an agent's report on a finding",
		Long: "Read what the agent concluded: its ratings, its verdict, and the report it wrote.\n\n" +
			"On a terminal the report is rendered for reading. Piped, or with -o markdown, it\n" +
			"is emitted as the markdown the agent actually wrote — paste it into an issue and\n" +
			"nothing is lost in translation.",
		Example: "  patchy review finding my-finding\n" +
			"  patchy review investigation --finding my-finding\n" +
			"  patchy review investigation --finding my-finding --attempt 2\n" +
			"  patchy review remediation my-finding-rem-1 --web\n" +
			"  patchy review finding my-finding -o markdown > report.md",
		Args:              cobra.RangeArgs(1, 2),
		ValidArgsFunction: nounCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := ""
			if len(args) > 1 {
				name = args[1]
			}
			return runReview(cmd.Context(), opts, f, args[0], name)
		},
	}

	fl := cmd.Flags()
	fl.StringVar(&f.finding, "finding", "", "select the run by its finding instead of by name")
	fl.Int32Var(&f.attempt, "attempt", 0, "which attempt to read (default: the latest)")
	fl.BoolVar(&f.raw, "raw", false, "keep the machine frontmatter the agent emitted")
	fl.BoolVar(&f.web, "web", false, "open the tracking issue (investigations) or pull request (remediations)")
	fl.BoolVar(&f.printURL, "print-url", false, "print that URL instead of opening it")
	return cmd
}

func runReview(ctx context.Context, opts *Options, f *reviewFlags, noun, name string) error {
	kind, err := resource.Lookup(noun)
	if err != nil {
		return errUsage(err)
	}
	if name == "" && f.finding == "" {
		return errUsage(fmt.Errorf("give a %s name or --finding", kind.Singular))
	}
	if !kind.Run() && kind.Singular != "finding" {
		return errUsage(fmt.Errorf("cannot review %s; try finding, investigation or remediation", kind.Plural))
	}

	env, err := opts.Connect()
	if err != nil {
		return err
	}
	p, err := opts.Printer()
	if err != nil {
		return err
	}
	callCtx, cancel := opts.Timeout(ctx)
	defer cancel()

	if kind.Singular == "finding" {
		return reviewFinding(callCtx, opts, env, p, f, name)
	}
	return reviewRun(callCtx, opts, env, p, f, kind, name)
}

// reviewFinding shows both stages of one finding together — the usual "what
// happened to this?" question, answered without having to know child names.
func reviewFinding(ctx context.Context, opts *Options, env *kubecfg.Env, p *printer.Printer,
	f *reviewFlags, name string,
) error {
	var fnd v1alpha1.Finding
	if err := env.Client.Get(ctx, objectKey(env, name), &fnd); err != nil {
		return err
	}
	if f.web || f.printURL {
		return openURL(opts, f, render.FindingURL(&fnd), "finding "+name)
	}

	d := p.Doc()
	render.FindingSummary(d, &fnd)

	found := false
	if s := fnd.Status.Investigation; s != nil {
		var inv v1alpha1.Investigation
		if err := env.Client.Get(ctx, objectKey(env, s.Name), &inv); err == nil {
			render.InvestigationReview(d, &inv, f.raw)
			found = true
		}
	}
	if s := fnd.Status.Remediation; s != nil {
		var rem v1alpha1.Remediation
		if err := env.Client.Get(ctx, objectKey(env, s.Name), &rem); err == nil {
			render.RemediationReview(d, &rem, f.raw)
			found = true
		}
	}
	if !found {
		notef(opts.ErrOut, "No agent has run on %s yet (phase %s).\n", fnd.Name, fnd.Status.Phase)
	}
	return d.Render()
}

// reviewRun shows one attempt of one stage.
func reviewRun(ctx context.Context, opts *Options, env *kubecfg.Env, p *printer.Printer,
	f *reviewFlags, kind resource.Kind, name string,
) error {
	name, err := resolveRunName(ctx, env, kind, f, name)
	if err != nil {
		return err
	}

	obj := kind.New()
	if err := env.Client.Get(ctx, objectKey(env, name), obj); err != nil {
		return err
	}

	d := p.Doc()
	switch typed := obj.(type) {
	case *v1alpha1.Investigation:
		if f.web || f.printURL {
			return openRunURL(ctx, opts, env, f, typed.Spec.FindingRef.Name, "", name)
		}
		render.InvestigationReview(d, typed, f.raw)
	case *v1alpha1.Remediation:
		if f.web || f.printURL {
			url := ""
			if pr := typed.Status.PullRequest; pr != nil {
				url = pr.URL
			}
			return openRunURL(ctx, opts, env, f, typed.Spec.FindingRef.Name, url, name)
		}
		render.RemediationReview(d, typed, f.raw)
	}
	return d.Render()
}

// resolveRunName turns --finding/--attempt into a child name. The controllers
// mint those names deterministically, so an explicit attempt is a direct Get
// and only "latest" has to consult the finding.
func resolveRunName(ctx context.Context, env *kubecfg.Env, kind resource.Kind,
	f *reviewFlags, name string,
) (string, error) {
	if name != "" {
		return name, nil
	}
	if f.attempt > 0 {
		return runName(f.finding, kind.Singular, f.attempt), nil
	}

	var fnd v1alpha1.Finding
	if err := env.Client.Get(ctx, objectKey(env, f.finding), &fnd); err != nil {
		return "", err
	}
	switch kind.Singular {
	case "investigation":
		if s := fnd.Status.Investigation; s != nil {
			return s.Name, nil
		}
	case "remediation":
		if s := fnd.Status.Remediation; s != nil {
			return s.Name, nil
		}
	}
	return "", fmt.Errorf("finding %s has no %s yet (phase %s)", f.finding, kind.Singular, fnd.Status.Phase)
}

// openRunURL opens the URL a run points at, falling back to the owning
// finding's tracking issue when the run has none of its own.
func openRunURL(ctx context.Context, opts *Options, env *kubecfg.Env, f *reviewFlags,
	findingName, url, label string,
) error {
	if url == "" {
		var fnd v1alpha1.Finding
		if err := env.Client.Get(ctx, objectKey(env, findingName), &fnd); err != nil {
			return err
		}
		url = render.FindingURL(&fnd)
	}
	return openURL(opts, f, url, label)
}

// openURL launches or prints a URL.
func openURL(opts *Options, f *reviewFlags, url, label string) error {
	if url == "" {
		return fmt.Errorf("%s has no URL to open yet", label)
	}
	if f.printURL {
		notef(opts.Out, "%s\n", url)
		return nil
	}
	opts.debugf("opening %s", url)
	return browser.Open(url)
}
