// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"k8s.io/client-go/util/retry"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/kubecfg"
	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/resource"
	"github.com/bitwise-media-group/patchy/internal/action"
)

// cliNote records where an approval came from, mirroring the status page's own
// note so the provenance of an approval is legible on the finding.
const cliNote = "Approved from the patchy CLI."

// verbHelp is the one-line description of each action.
var verbHelp = map[string]string{
	action.VerbApprove:  "Approve a finding, releasing a hold or reviving a handed-off finding",
	action.VerbRetry:    "Retry a failed finding from the state it failed in",
	action.VerbExpedite: "Expedite a finding past the accumulation window and the queue",
	action.VerbSuspend:  "Suspend a finding, pausing its progress through the pipeline",
	action.VerbResume:   "Resume a suspended finding",
}

// actionFlags are the options every action verb accepts.
type actionFlags struct {
	note     string
	selector string
	dryRun   bool
	yes      bool
}

// newActionCmds builds one command per action verb. They differ only in the
// verb they carry, so they are generated rather than written five times.
func newActionCmds(opts *Options) []*cobra.Command {
	cmds := make([]*cobra.Command, 0, len(action.ActionVerbs))
	for _, verb := range action.ActionVerbs {
		cmds = append(cmds, newActionCmd(opts, verb))
	}
	return cmds
}

func newActionCmd(opts *Options, verb string) *cobra.Command {
	f := &actionFlags{}
	cmd := &cobra.Command{
		Use:   fmt.Sprintf("%s finding <name...>", verb),
		Short: verbHelp[verb],
		Long: verbHelp[verb] + ".\n\n" +
			"The action writes to the finding's spec only — a controller observes it and\n" +
			"moves the phase. Repeating an action that has already taken effect is a no-op,\n" +
			"so this is safe to re-run.\n\n" +
			"Your RBAC decides whether it lands: the CLI checks the '" + verb + "' verb before\n" +
			"writing, and the cluster's admission policy enforces it regardless of client.",
		Example: fmt.Sprintf("  patchy %s finding my-finding\n"+
			"  patchy %s finding -l patchy.bitwisemedia.uk/severity=critical\n"+
			"  patchy %s finding my-finding --dry-run", verb, verb, verb),
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: nounCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAction(cmd.Context(), opts, f, verb, args[0], args[1:])
		},
	}

	fl := cmd.Flags()
	if verb == action.VerbApprove {
		fl.StringVar(&f.note, "note", cliNote, "note to record with the approval")
	}
	fl.StringVarP(&f.selector, "selector", "l", "", "act on every finding matching this label selector")
	fl.BoolVar(&f.dryRun, "dry-run", false, "report what would change without writing")
	fl.BoolVarP(&f.yes, "yes", "y", false, "do not prompt when acting on more than one finding")
	return cmd
}

func runAction(ctx context.Context, opts *Options, f *actionFlags, verb, noun string, names []string) error {
	kind, err := resource.Lookup(noun)
	if err != nil {
		return errUsage(err)
	}
	if kind.Singular != "finding" {
		return errUsage(fmt.Errorf("%s applies to findings, not %s", verb, kind.Plural))
	}
	if len(names) == 0 && f.selector == "" {
		return errUsage(fmt.Errorf("name at least one finding, or select them with -l"))
	}

	env, err := opts.Connect()
	if err != nil {
		return err
	}
	if env.Namespace == "" {
		return errUsage(errors.New("actions need a single namespace; drop --all-namespaces or pass -n"))
	}

	// Ask before writing. The admission policy is what actually enforces this,
	// but a local check turns "the server rejected your update" into a sentence
	// that names the verb you are missing.
	allowed, err := opts.access(ctx, env, verb)
	if err != nil {
		return err
	}
	if !allowed {
		return fmt.Errorf("%w: you may not %s findings in namespace %s",
			errDenied, verb, env.Namespace)
	}

	// spec.approval.by and spec.retry.by are the audit trail, so they have to
	// name the real identity rather than whatever the caller typed.
	user := opts.identity(ctx, env)
	opts.debugf("acting as %s", user)

	targets, err := actionTargets(ctx, opts, env, f, names)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		notef(opts.ErrOut, "No findings matched.\n")
		return nil
	}
	if !f.yes && !f.dryRun && len(targets) > 1 {
		if err := confirm(opts, verb, targets); err != nil {
			return err
		}
	}

	return applyToAll(ctx, opts, env, f, verb, user, targets)
}

// actionTargets resolves the findings to act on, by name or by selector.
func actionTargets(ctx context.Context, opts *Options, env *kubecfg.Env,
	f *actionFlags, names []string,
) ([]string, error) {
	if len(names) > 0 {
		return names, nil
	}
	callCtx, cancel := opts.Timeout(ctx)
	defer cancel()

	var list v1alpha1.FindingList
	if err := env.Client.List(callCtx, &list, listOptions(env, f.selector)...); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, list.Items[i].Name)
	}
	return out, nil
}

// applyToAll runs the verb against every target, continuing past failures so
// one unavailable finding does not hide the rest. The worst outcome across the
// batch becomes the exit code.
func applyToAll(ctx context.Context, opts *Options, env *kubecfg.Env,
	f *actionFlags, verb, user string, targets []string,
) error {
	var failures []error
	for _, name := range targets {
		changed, err := applyOne(ctx, opts, env, f, verb, user, name)
		switch {
		case err != nil:
			notef(opts.ErrOut, "%s: %v\n", name, err)
			failures = append(failures, err)
		case f.dryRun && changed:
			notef(opts.Out, "%s would be %s\n", name, past(verb))
		case f.dryRun:
			notef(opts.Out, "%s is already %s; nothing to do\n", name, past(verb))
		case changed:
			notef(opts.Out, "%s %s\n", name, past(verb))
		default:
			notef(opts.Out, "%s was already %s\n", name, past(verb))
		}
	}
	if len(failures) > 0 {
		// Propagate one failure so exitCode can classify the run; the detail
		// of each is already on stderr.
		return fmt.Errorf("%d of %d findings failed: %w", len(failures), len(targets), failures[0])
	}
	return nil
}

// applyOne applies the verb to one finding under conflict retry. The gating is
// re-evaluated on every attempt because a re-Get may have changed the phase the
// decision rests on.
func applyOne(ctx context.Context, opts *Options, env *kubecfg.Env,
	f *actionFlags, verb, user, name string,
) (bool, error) {
	changed := false
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		callCtx, cancel := opts.Timeout(ctx)
		defer cancel()

		var fnd v1alpha1.Finding
		if err := env.Client.Get(callCtx, objectKey(env, name), &fnd); err != nil {
			return err
		}
		var err error
		changed, err = action.Apply(&fnd, verb, user, f.note, now())
		if err != nil {
			return fmt.Errorf("%w (phase %s)", err, fnd.Status.Phase)
		}
		if !changed || f.dryRun {
			return nil
		}
		return env.Client.Update(callCtx, &fnd)
	})
	return changed, err
}

// confirm asks before a bulk write. Naming the count and the verb is the whole
// point — a selector that matched more than expected is the mistake this
// catches.
func confirm(opts *Options, verb string, targets []string) error {
	notef(opts.ErrOut, "About to %s %d findings:\n", verb, len(targets))
	for _, t := range targets {
		notef(opts.ErrOut, "  %s\n", t)
	}
	notef(opts.ErrOut, "Continue? [y/N] ")

	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil || !strings.EqualFold(strings.TrimSpace(line), "y") {
		return errors.New("cancelled")
	}
	return nil
}

// past renders a verb as its past participle, for result lines like
// "fnd-1 suspended".
func past(verb string) string {
	switch verb {
	case action.VerbRetry:
		return "retried"
	case action.VerbSuspend:
		return "suspended"
	default:
		// approve, resume and expedite all end in 'e'.
		return verb + "d"
	}
}
