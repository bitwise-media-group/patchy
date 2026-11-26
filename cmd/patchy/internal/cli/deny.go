// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/kubecfg"
	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/resource"
	"github.com/bitwise-media-group/patchy/internal/action"
)

// maxDecisionReason mirrors the CRD's bound on spec.decision.reason, so an
// over-long reason fails here with a clear message rather than at the API.
const maxDecisionReason = 500

// errNotDecidable marks a backfill request whose state has no use for a
// decision: it was never filed for approval, or an operator already decided
// it the other way. Like an unavailable action, nothing was written.
var errNotDecidable = errors.New("backfill request cannot be decided")

// decisionFlags are the options approve and deny accept for backfill requests.
type decisionFlags struct {
	reason string
	dryRun bool
}

func newDenyCmd(opts *Options) *cobra.Command {
	f := &decisionFlags{}
	cmd := &cobra.Command{
		Use:   "deny backfillrequest <name...>",
		Short: "Deny a backfill request that is awaiting an operator",
		Long: "Deny a backfill request filed with `patchy backfill --request`. The request\n" +
			"moves to Denied and never runs. --reason is shown to the requester.\n\n" +
			"The command writes only the request's spec.decision, and a decision is final:\n" +
			"a request that was already approved cannot be denied. Repeating a denial does\n" +
			"nothing, so it is safe to re-run. `patchy approve backfillrequest <name>` is\n" +
			"the other half.\n\n" +
			"Deciding a request needs the 'backfill' verb on integrations. The CLI checks it\n" +
			"before writing, and the cluster's admission policy enforces it for every\n" +
			"client.",
		Example: "  patchy deny backfillrequest backfill-x7k2p\n" +
			"  patchy deny bfr backfill-x7k2p --reason \"the whole org is too broad; ask for acme/shop\"",
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: decisionNounCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			kind, err := resource.Lookup(args[0])
			if err != nil {
				return errUsage(err)
			}
			if kind.Singular != "backfillrequest" {
				return errUsage(fmt.Errorf("deny applies to backfill requests, not %s", kind.Plural))
			}
			return runDecision(cmd.Context(), opts, f, v1alpha1.BackfillDeniedVerdict, args[1:])
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.reason, "reason", "", "why the request was denied, shown to the requester")
	fl.BoolVar(&f.dryRun, "dry-run", false, "report what would change without writing")
	return cmd
}

// runDecision records an operator's verdict on each named backfill request.
// approve and deny share it; they differ only in the verdict.
func runDecision(ctx context.Context, opts *Options, f *decisionFlags,
	verdict v1alpha1.BackfillVerdict, names []string,
) error {
	if len(names) == 0 {
		return errUsage(errors.New("name at least one backfill request"))
	}
	if len(f.reason) > maxDecisionReason {
		return errUsage(fmt.Errorf("--reason is %d characters; the limit is %d", len(f.reason), maxDecisionReason))
	}

	env, err := opts.Connect()
	if err != nil {
		return err
	}
	if env.Namespace == "" {
		return errUsage(errors.New("deciding backfill requests needs a single namespace; " +
			"drop --all-namespaces or pass -n"))
	}

	// Deciding is an operator's call: the same backfill verb on integrations
	// that lets a request run at once. Ask before writing, as the action verbs
	// do, so a refusal names the missing verb.
	allowed, err := opts.access(ctx, env, action.ResourceIntegrations, action.VerbBackfill)
	if err != nil {
		return err
	}
	if !allowed {
		return fmt.Errorf("%w: deciding backfill requests in namespace %s needs the '%s' verb on integrations",
			errDenied, env.Namespace, action.VerbBackfill)
	}

	// spec.decision.by is the audit trail, so it names the real identity.
	user := opts.identity(ctx, env)
	opts.debugf("acting as %s", user)

	word := decisionPast(verdict)
	var failures []error
	for _, name := range names {
		changed, err := decideOne(ctx, opts, env, f, verdict, user, name)
		switch {
		case err != nil:
			notef(opts.ErrOut, "%s: %v\n", name, err)
			failures = append(failures, err)
		case f.dryRun && changed:
			notef(opts.Out, "%s would be %s\n", name, word)
		case f.dryRun:
			notef(opts.Out, "%s is already %s; nothing to do\n", name, word)
		case changed:
			notef(opts.Out, "%s %s\n", name, word)
		default:
			notef(opts.Out, "%s was already %s\n", name, word)
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("%d of %d backfill requests failed: %w", len(failures), len(names), failures[0])
	}
	return nil
}

// decideOne records the verdict on one request under conflict retry. The
// checks re-run on every attempt, because a re-Get may show that another
// operator decided first.
func decideOne(ctx context.Context, opts *Options, env *kubecfg.Env, f *decisionFlags,
	verdict v1alpha1.BackfillVerdict, user, name string,
) (bool, error) {
	changed := false
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		callCtx, cancel := opts.Timeout(ctx)
		defer cancel()

		var req v1alpha1.BackfillRequest
		if err := env.Client.Get(callCtx, objectKey(env, name), &req); err != nil {
			return err
		}
		var err error
		changed, err = decide(&req, verdict, user, f.reason, metav1.NewTime(now()))
		if err != nil || !changed || f.dryRun {
			return err
		}
		return env.Client.Update(callCtx, &req)
	})
	return changed, err
}

// decide records verdict on req's spec, reporting whether it changed. Only a
// request filed for approval takes a decision, and only once: repeating the
// same verdict is a no-op, and contradicting an earlier one is refused.
func decide(req *v1alpha1.BackfillRequest, verdict v1alpha1.BackfillVerdict,
	user, reason string, at metav1.Time,
) (bool, error) {
	if req.Spec.Via != v1alpha1.BackfillViaRequested {
		via := req.Spec.Via
		if via == "" {
			via = v1alpha1.BackfillViaGrant
		}
		return false, fmt.Errorf("%w: it was filed via %q, so it runs without a decision; "+
			"only a request filed with `patchy backfill --request` waits for an operator",
			errNotDecidable, via)
	}
	if prior := req.Spec.Decision; prior != nil {
		if prior.Verdict == verdict {
			return false, nil
		}
		return false, fmt.Errorf("%w: it was already %s by %s, and a decision is final",
			errNotDecidable, strings.ToLower(string(prior.Verdict)), prior.By)
	}
	req.Spec.Decision = &v1alpha1.BackfillDecision{
		Verdict: verdict,
		By:      user,
		At:      at,
		Reason:  reason,
	}
	return true, nil
}

// decisionPast renders a verdict for result lines like "backfill-x7k2p denied".
func decisionPast(v v1alpha1.BackfillVerdict) string {
	return strings.ToLower(string(v))
}

// decisionNounCompletion completes the noun of a decision: backfill requests
// are the only kind a decision applies to.
func decisionNounCompletion(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return []string{"backfillrequest", "backfillrequests", "bfr"}, cobra.ShellCompDirectiveNoFileComp
}
