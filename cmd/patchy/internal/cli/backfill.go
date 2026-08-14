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
		Long: "Request a one-shot list-alerts backfill on an integration: the\n" +
			"integration-controller walks the provider's open alerts and ingests the ones\n" +
			"that predate webhook coverage — alerts raised before the GitHub App was\n" +
			"installed on (or associated with) their repository, which no replay of the\n" +
			"delivery log can ever surface. Ingestion is idempotent, so alerts that already\n" +
			"have findings simply fold in.\n\n" +
			"--repo narrows the walk: an \"owner/\" prefix covers a whole account, an\n" +
			"\"owner/name\" entry one repository (and, being a prefix, any name extending\n" +
			"it). With App credentials the filter prunes whole installations; with a PAT\n" +
			"every entry must be an exact owner/name, since a PAT cannot discover\n" +
			"repositories. A walk that reports truncated on status.backfill hit the page\n" +
			"budget — re-run with a narrower prefix to reach the rest.\n\n" +
			"The command writes spec.backfill only — a controller observes it and runs the\n" +
			"walk. Your RBAC decides whether it lands: the CLI checks the 'backfill' verb on\n" +
			"integrations before writing, and the cluster's admission policy enforces it\n" +
			"regardless of client.",
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

	// Ask before writing. The admission policy is what actually enforces
	// this; the local check turns a rejected update into a sentence naming
	// the missing verb.
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
