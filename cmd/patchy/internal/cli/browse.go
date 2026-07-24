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

func newBrowseCmd(opts *Options) *cobra.Command {
	var printURL bool
	cmd := &cobra.Command{
		Use:   "browse <resource> <name>",
		Short: "Browse to a resource's page in a browser",
		Long: "Open the human-facing page for a resource in your browser: the tracking issue\n" +
			"for a finding or investigation, the pull request for a remediation, the\n" +
			"repository for a repository.\n\n" +
			"The verb is `browse` rather than `open` because every other patchy verb acts on\n" +
			"the pipeline — and `open` would read as moving a finding into the Opened phase.",
		Example: "  patchy browse finding my-finding\n" +
			"  patchy browse remediation my-finding-rem-1\n" +
			"  patchy browse finding my-finding --print-url",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: nounCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBrowse(cmd.Context(), opts, args[0], args[1], printURL)
		},
	}
	cmd.Flags().BoolVar(&printURL, "print-url", false, "print the URL instead of opening it")
	return cmd
}

func runBrowse(ctx context.Context, opts *Options, noun, name string, printURL bool) error {
	kind, err := resource.Lookup(noun)
	if err != nil {
		return errUsage(err)
	}
	env, err := opts.Connect()
	if err != nil {
		return err
	}
	callCtx, cancel := opts.Timeout(ctx)
	defer cancel()

	obj := kind.New()
	if err := env.Client.Get(callCtx, objectKey(env, name), obj); err != nil {
		return err
	}
	url, err := resourceURL(callCtx, env, obj)
	if err != nil {
		return err
	}
	return openURL(opts, &reviewFlags{printURL: printURL}, url, fmt.Sprintf("%s %s", kind.Singular, name))
}

// resourceURL picks the destination that matters for each kind: where a human
// would go to act on it.
func resourceURL(ctx context.Context, env *kubecfg.Env, obj any) (string, error) {
	switch typed := obj.(type) {
	case *v1alpha1.Finding:
		return render.FindingURL(typed), nil
	case *v1alpha1.Investigation:
		// An investigation has no page of its own; its finding's issue is
		// where its conclusions were posted.
		return owningFindingURL(ctx, env, typed.Spec.FindingRef.Name)
	case *v1alpha1.Remediation:
		if pr := typed.Status.PullRequest; pr != nil && pr.URL != "" {
			return pr.URL, nil
		}
		return owningFindingURL(ctx, env, typed.Spec.FindingRef.Name)
	case *v1alpha1.Repository:
		return typed.Spec.URL, nil
	default:
		return "", fmt.Errorf("nothing to open for this resource")
	}
}

// owningFindingURL resolves a child's finding and returns its tracking issue.
func owningFindingURL(ctx context.Context, env *kubecfg.Env, name string) (string, error) {
	var fnd v1alpha1.Finding
	if err := env.Client.Get(ctx, objectKey(env, name), &fnd); err != nil {
		return "", err
	}
	return render.FindingURL(&fnd), nil
}
