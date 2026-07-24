// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/kubecfg"
	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/render"
	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/resource"
)

func newDescribeCmd(opts *Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "describe <resource> <name>",
		Short:   "Show the full detail of one resource",
		Long:    "Show everything known about one resource: state, timeline, and what can be done to it.",
		Example: "  patchy describe finding my-finding\n  patchy describe investigation my-finding-inv-1",
		Args:    cobra.ExactArgs(2),

		ValidArgsFunction: nounCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDescribe(cmd.Context(), opts, args[0], args[1])
		},
	}
	return cmd
}

func runDescribe(ctx context.Context, opts *Options, noun, name string) error {
	kind, err := resource.Lookup(noun)
	if err != nil {
		return errUsage(err)
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

	obj := kind.New()
	if err := env.Client.Get(callCtx, objectKey(env, name), obj); err != nil {
		return err
	}
	if p.Format().Structured() {
		ref := fmt.Sprintf("%s.%s/%s", kind.Plural, v1alpha1.GroupVersion.Group, name)
		return p.Objects([]any{obj}, []string{ref})
	}

	d := p.Doc()
	switch typed := obj.(type) {
	case *v1alpha1.Finding:
		// Spend lives on the child runs, so it needs a query the render layer
		// deliberately cannot make. A failure to total it is not worth failing
		// the whole view over — the rest of the finding is still useful.
		spend, err := findingSpend(callCtx, env, typed)
		if err != nil {
			opts.debugf("could not total spend for %s: %v", typed.Name, err)
		}
		render.FindingDetail(d, typed, now(), spend)
	case *v1alpha1.Investigation:
		render.InvestigationDetail(d, typed, now())
	case *v1alpha1.Remediation:
		render.RemediationDetail(d, typed, now())
	default:
		// Every other kind is configuration, not state: its spec is the
		// interesting part and YAML shows it better than a bespoke view.
		return p.Objects([]any{obj}, nil)
	}
	return d.Render()
}

// findingSpend totals the cost across every attempt of both stages. The figures
// live on the Investigation and Remediation children, which outlive nothing and
// may already have been collected — a missing child simply contributes nothing.
func findingSpend(ctx context.Context, env *kubecfg.Env, f *v1alpha1.Finding) (string, error) {
	sel := listOptions(env, fmt.Sprintf("%s=%s", v1alpha1.LabelFinding, f.Name))

	var invs v1alpha1.InvestigationList
	if err := env.Client.List(ctx, &invs, sel...); err != nil {
		return "", err
	}
	var rems v1alpha1.RemediationList
	if err := env.Client.List(ctx, &rems, sel...); err != nil {
		return "", err
	}

	var in, out int64
	var cost float64
	add := func(st *v1alpha1.StageResult) {
		if st == nil {
			return
		}
		in += st.Usage.InputTokens
		out += st.Usage.OutputTokens
		var c float64
		if _, err := fmt.Sscanf(st.Usage.CostUSD, "%g", &c); err == nil {
			cost += c
		}
	}
	for i := range invs.Items {
		add(invs.Items[i].Status.Stage)
	}
	for i := range rems.Items {
		add(rems.Items[i].Status.Stage)
	}
	if in == 0 && out == 0 && cost == 0 {
		return "", nil
	}
	return fmt.Sprintf("%d in / %d out tokens, $%.4f across %d runs",
		in, out, cost, len(invs.Items)+len(rems.Items)), nil
}
