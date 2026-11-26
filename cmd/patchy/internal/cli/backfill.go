// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/printer"
	"github.com/bitwise-media-group/patchy/internal/action"
)

// backfillFlags are the options `backfill` accepts.
type backfillFlags struct {
	repos   []string
	request bool
}

func newBackfillCmd(opts *Options) *cobra.Command {
	f := &backfillFlags{}
	cmd := &cobra.Command{
		Use:   "backfill <integration>",
		Short: "Backfill an integration's pre-existing open alerts into findings",
		Long: "File a one-shot backfill of open alerts on an integration. The command creates\n" +
			"a BackfillRequest. The integration-controller walks the provider's open alerts\n" +
			"and ingests the ones that predate webhook coverage. These are alerts raised\n" +
			"before the GitHub App was installed on (or associated with) their repository,\n" +
			"so no replay of the delivery log can surface them. Ingestion is idempotent: an\n" +
			"alert that already has a finding merges into it.\n\n" +
			"--repo narrows the walk. An \"owner/\" prefix covers a whole account. An\n" +
			"\"owner/name\" entry covers one repository and, because it is a prefix, any name\n" +
			"that starts with it. With App credentials the filter skips whole installations.\n" +
			"With a PAT every entry must be an exact owner/name, because a PAT cannot\n" +
			"discover repositories. If the request reports the walk as truncated, it hit\n" +
			"the page budget: file another with a narrower prefix to reach the rest.\n\n" +
			"A request that runs at once needs the 'backfill' verb on integrations. The CLI\n" +
			"checks it before writing, and the cluster's admission policy enforces it for\n" +
			"every client. Without the verb, pass --request: the request then waits in\n" +
			"AwaitingApproval until an operator approves it (`patchy approve\n" +
			"backfillrequest <name>`) or denies it (`patchy deny backfillrequest <name>`).\n\n" +
			"Watch a request with `patchy get backfillrequests` or `patchy describe\n" +
			"backfillrequest <name>`.",
		Example: "  patchy backfill gh\n" +
			"  patchy backfill gh --repo acme/\n" +
			"  patchy backfill gh --repo acme/shop --repo acme/billing\n" +
			"  patchy backfill gh --repo acme/shop --request",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBackfill(cmd.Context(), opts, f, args[0])
		},
	}
	fl := cmd.Flags()
	fl.StringArrayVar(&f.repos, "repo", nil,
		"repository filter: an \"owner/\" prefix or an exact \"owner/name\" (repeatable; empty = full scope)")
	fl.BoolVar(&f.request, "request", false,
		"ask an operator to approve the backfill instead of running it at once (needs no backfill verb)")
	return cmd
}

func runBackfill(ctx context.Context, opts *Options, f *backfillFlags, name string) error {
	for _, entry := range f.repos {
		if strings.TrimSpace(entry) == "" || strings.ContainsAny(entry, " \t") {
			return errUsage(fmt.Errorf("invalid --repo entry %q; want \"owner/\" or \"owner/name\"", entry))
		}
	}

	env, err := opts.Connect()
	if err != nil {
		return err
	}
	if env.Namespace == "" {
		return errUsage(fmt.Errorf("backfill needs a single namespace; drop --all-namespaces or pass -n"))
	}

	// Ask before writing. The admission policy enforces both checks, but the
	// local ones turn a rejected create into a message naming what is missing.
	via := v1alpha1.BackfillViaGrant
	if f.request {
		via = v1alpha1.BackfillViaRequested
	} else {
		allowed, err := opts.access(ctx, env, action.ResourceIntegrations, action.VerbBackfill)
		if err != nil {
			return err
		}
		if !allowed {
			return fmt.Errorf("%w: you may not backfill integrations in namespace %s; "+
				"re-run with --request to ask an operator to approve it",
				errDenied, env.Namespace)
		}
	}
	allowed, err := opts.access(ctx, env, "backfillrequests", "create")
	if err != nil {
		return err
	}
	if !allowed {
		return fmt.Errorf("%w: you may not create backfillrequests in namespace %s",
			errDenied, env.Namespace)
	}

	// spec.requestedBy is the audit trail, and the admission policy requires
	// it to be the creating user, so it names the real identity.
	user := opts.identity(ctx, env)
	opts.debugf("acting as %s", user)

	callCtx, cancel := opts.Timeout(ctx)
	defer cancel()

	var integ v1alpha1.Integration
	if err := env.Client.Get(callCtx, objectKey(env, name), &integ); err != nil {
		return err
	}
	if integ.Spec.Suspend {
		return fmt.Errorf("integration %s is suspended; resume it before backfilling", name)
	}

	req := &v1alpha1.BackfillRequest{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "backfill-",
			Namespace:    env.Namespace,
			Labels:       map[string]string{v1alpha1.LabelIntegration: name},
		},
		Spec: v1alpha1.BackfillRequestSpec{
			IntegrationRef: v1alpha1.LocalObjectReference{Name: name},
			RequestedBy:    user,
			Repositories:   f.repos,
		},
	}
	// The grant path leaves via empty, which the API reads as "backfill".
	if via == v1alpha1.BackfillViaRequested {
		req.Spec.Via = via
	}
	if err := env.Client.Create(callCtx, req); err != nil {
		return err
	}

	// -o name prints only the reference, so a script can capture it.
	if format, err := printer.ParseFormat(opts.Output); err == nil && format == printer.FormatName {
		notef(opts.Out, "backfillrequests.%s/%s\n", v1alpha1.GroupVersion.Group, req.Name)
		return nil
	}
	if f.request {
		notef(opts.Out, "Backfill request %s filed on integration %s. It waits in AwaitingApproval\n"+
			"until an operator approves it:\n"+
			"  patchy approve backfillrequest %s\n",
			req.Name, name, req.Name)
	} else {
		notef(opts.Out, "Backfill request %s created on integration %s.\n", req.Name, name)
	}
	notef(opts.Out, "Watch it with:\n"+
		"  patchy get backfillrequests %s\n"+
		"  kubectl get backfillrequests %s\n",
		req.Name, req.Name)
	return nil
}
