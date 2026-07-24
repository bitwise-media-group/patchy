// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/kubecfg"
	"github.com/bitwise-media-group/patchy/internal/action"
)

func newCanICmd(opts *Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "can-i [verb]",
		Short: "Show which actions your RBAC allows",
		Long: "Show what you may do to findings in this namespace.\n\n" +
			"Each action is a custom RBAC verb, granted independently — holding 'approve'\n" +
			"says nothing about 'suspend'. With no argument this prints the whole matrix,\n" +
			"which is the fastest answer to \"why was that refused?\".",
		Example: "  patchy can-i\n  patchy can-i approve\n  patchy can-i approve -n patchy",
		Args:    cobra.MaximumNArgs(1),
		ValidArgsFunction: func(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
			if len(args) > 0 {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			return action.ActionVerbs, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			verb := ""
			if len(args) == 1 {
				verb = args[0]
			}
			return runCanI(cmd.Context(), opts, verb)
		},
	}
	return cmd
}

func runCanI(ctx context.Context, opts *Options, verb string) error {
	env, err := opts.Connect()
	if err != nil {
		return err
	}
	if env.Namespace == "" {
		return errUsage(fmt.Errorf("can-i needs a single namespace; drop --all-namespaces or pass -n"))
	}

	// A single verb answers yes/no and says so in the exit code, so it can
	// drive a shell conditional.
	if verb != "" {
		if !containsFold(action.ActionVerbs, verb) && verb != "get" && verb != "list" {
			return errUsage(fmt.Errorf("unknown verb %q; want one of: %s, get, list",
				verb, strings.Join(action.ActionVerbs, ", ")))
		}
		allowed, err := opts.access(ctx, env, verb)
		if err != nil {
			return err
		}
		if !allowed {
			notef(opts.Out, "no\n")
			return fmt.Errorf("%w: you may not %s findings in namespace %s", errDenied, verb, env.Namespace)
		}
		notef(opts.Out, "yes\n")
		return nil
	}

	p, err := opts.Printer()
	if err != nil {
		return err
	}
	d := p.Doc()
	d.Section(fmt.Sprintf("Grants in namespace %s", env.Namespace)).
		Field("Identity", opts.identity(ctx, env))

	for _, v := range append([]string{"get", "list"}, action.ActionVerbs...) {
		allowed, err := opts.access(ctx, env, v)
		if err != nil {
			return err
		}
		d.Field(v, yesNo(allowed))
	}
	return d.Render()
}

// whoami asks the API server which identity this kubeconfig authenticates as.
//
// Every authenticated user may perform this review about themselves, so it
// needs no grant. It is worth a round trip because the answer is what lands in
// spec.approval.by — a kubeconfig's context name is a local label and often has
// nothing to do with the username the cluster sees.
func whoami(ctx context.Context, opts *Options, env *kubecfg.Env) string {
	callCtx, cancel := opts.Timeout(ctx)
	defer cancel()

	review := &authenticationv1.SelfSubjectReview{}
	if err := env.Client.Create(callCtx, review); err != nil {
		opts.debugf("self subject review failed: %v", err)
		return "unknown"
	}
	if name := review.Status.UserInfo.Username; name != "" {
		return name
	}
	return "unknown"
}

// canI runs a SelfSubjectAccessReview for one verb on findings.
//
// Self- rather than plain SubjectAccessReview matters: asking about yourself
// needs no grant, whereas asking about another user is privileged and the CLI
// has no business doing it.
func canI(ctx context.Context, opts *Options, env *kubecfg.Env, verb string) (bool, error) {
	callCtx, cancel := opts.Timeout(ctx)
	defer cancel()

	review := &authorizationv1.SelfSubjectAccessReview{
		Spec: authorizationv1.SelfSubjectAccessReviewSpec{
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Namespace: env.Namespace,
				Group:     v1alpha1.GroupVersion.Group,
				Resource:  "findings",
				Verb:      verb,
			},
		},
	}
	if err := env.Client.Create(callCtx, review); err != nil {
		return false, fmt.Errorf("access review for %s: %w", verb, err)
	}
	opts.debugf("access review: %s = %t (%s)", verb, review.Status.Allowed, review.Status.Reason)
	return review.Status.Allowed, nil
}

// yesNo renders a grant.
func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
