// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/action"
)

func newBackfillCmd(opts *Options) *cobra.Command {
	var repos []string
	cmd := &cobra.Command{
		Use:   "backfill <integration>",
		Short: "Backfill an integration's pre-existing open alerts into findings",
		Long: "Request a one-shot backfill of open alerts on an integration. The\n" +
			"integration-controller lists the provider's open alerts and ingests the ones\n" +
			"that predate webhook coverage. These are alerts raised before the GitHub App\n" +
			"was installed on (or associated with) their repository, so no replay of the\n" +
			"delivery log can surface them. Ingestion is idempotent: an alert that already\n" +
			"has a finding merges into it.\n\n" +
			"--repo narrows the walk. An \"owner/\" prefix covers a whole account. An\n" +
			"\"owner/name\" entry covers one repository and, because it is a prefix, any name\n" +
			"that starts with it. With App credentials the filter skips whole installations.\n" +
			"With a PAT every entry must be an exact owner/name, because a PAT cannot\n" +
			"discover repositories. If status.backfill reports the walk as truncated, it hit\n" +
			"the page budget: re-run with a narrower prefix to reach the rest.\n\n" +
			"The command writes only spec.backfill. A controller sees the change and runs\n" +
			"the walk. Your RBAC decides whether the write is allowed. The CLI checks the\n" +
			"'backfill' verb on integrations before writing, and the cluster's admission\n" +
			"policy enforces it for every client.",
		Example: "  patchy backfill gh\n" +
			"  patchy backfill gh --repo acme/\n" +
			"  patchy backfill gh --repo acme/shop --repo acme/billing",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBackfill(cmd.Context(), opts, args[0], repos)
		},
	}
	cmd.Flags().StringArrayVar(&repos, "repo", nil,
		"repository filter: an \"owner/\" prefix or an exact \"owner/name\" (repeatable; empty = full scope)")
	return cmd
}

func runBackfill(ctx context.Context, opts *Options, name string, repos []string) error {
	for _, entry := range repos {
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

	// Ask before writing. The admission policy enforces this, but the local
	// check turns a rejected update into a message naming the missing verb.
	allowed, err := opts.access(ctx, env, "integrations", action.VerbBackfill)
	if err != nil {
		return err
	}
	if !allowed {
		return fmt.Errorf("%w: you may not backfill integrations in namespace %s",
			errDenied, env.Namespace)
	}

	// spec.backfill.by is the audit trail, so it names the real identity.
	user := opts.identity(ctx, env)
	opts.debugf("acting as %s", user)

	callCtx, cancel := opts.Timeout(ctx)
	defer cancel()
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var integ v1alpha1.Integration
		key := types.NamespacedName{Namespace: env.Namespace, Name: name}
		if err := env.Client.Get(callCtx, key, &integ); err != nil {
			return err
		}
		if integ.Spec.Suspend {
			return fmt.Errorf("integration %s is suspended", name)
		}
		integ.Spec.Backfill = &v1alpha1.BackfillRequest{
			By:           user,
			At:           metav1.Now(),
			Repositories: repos,
		}
		return env.Client.Update(callCtx, &integ)
	})
	if err != nil {
		return err
	}
	notef(opts.Out, "Backfill requested on integration %s; watch status.backfill for the outcome.\n", name)
	return nil
}
