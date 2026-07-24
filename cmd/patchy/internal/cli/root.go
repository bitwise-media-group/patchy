// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/kubecfg"
	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/printer"
	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/resource"
	"github.com/bitwise-media-group/patchy/internal/version"
)

// defaultTimeout bounds a single API call. Generous enough for a slow VPN,
// short enough that a wedged cluster does not hang a shell.
const defaultTimeout = 30 * time.Second

// Options carries the persistent flags and the lazily-resolved cluster
// connection. One instance is shared by the whole command tree.
type Options struct {
	Kubeconfig     string
	Context        string
	Namespace      string
	AllNamespaces  bool
	Output         string
	NoColor        bool
	RequestTimeout time.Duration
	Verbose        bool

	// Out and ErrOut are the streams commands write to. Held here rather than
	// reached for as globals so tests can capture them.
	Out    io.Writer
	ErrOut io.Writer

	env *kubecfg.Env
	// accessFn and identityFn stand in for the two API calls that ask the
	// server about the caller. A fake client answers neither (it has no
	// authorizer and no authenticated identity), so tests supply them.
	accessFn   func(context.Context, *kubecfg.Env, string) (bool, error)
	identityFn func() string
}

// WithEnv pins a pre-built environment, bypassing kubeconfig resolution.
func (o *Options) WithEnv(env *kubecfg.Env) { o.env = env }

// WithAccess pins the access-review answer.
func (o *Options) WithAccess(fn func(context.Context, *kubecfg.Env, string) (bool, error)) {
	o.accessFn = fn
}

// WithIdentity pins the identity actions are recorded under.
func (o *Options) WithIdentity(fn func() string) { o.identityFn = fn }

// access reports whether the caller holds verb on findings.
func (o *Options) access(ctx context.Context, env *kubecfg.Env, verb string) (bool, error) {
	if o.accessFn != nil {
		return o.accessFn(ctx, env, verb)
	}
	return canI(ctx, o, env, verb)
}

// identity is who the cluster says the caller is.
func (o *Options) identity(ctx context.Context, env *kubecfg.Env) string {
	if o.identityFn != nil {
		return o.identityFn()
	}
	return whoami(ctx, o, env)
}

// Connect resolves the cluster connection, once per process.
func (o *Options) Connect() (*kubecfg.Env, error) {
	if o.env != nil {
		return o.env, nil
	}
	env, err := kubecfg.Connect(kubecfg.Options{
		Kubeconfig:    o.Kubeconfig,
		Context:       o.Context,
		Namespace:     o.Namespace,
		AllNamespaces: o.AllNamespaces,
	})
	if err != nil {
		return nil, err
	}
	o.env = env
	return env, nil
}

// Printer builds the printer for the resolved output format.
func (o *Options) Printer() (*printer.Printer, error) {
	format, err := printer.ParseFormat(o.Output)
	if err != nil {
		return nil, errUsage(err)
	}
	return printer.New(o.Out, format, printer.Color(o.Out, o.NoColor)), nil
}

// Timeout derives a per-call context from the command's context.
func (o *Options) Timeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, o.RequestTimeout)
}

// debugf narrates to stderr under --verbose. Nothing here may go to stdout:
// stdout is reserved for data a caller might pipe.
func (o *Options) debugf(format string, args ...any) {
	if o.Verbose {
		notef(o.ErrOut, "patchy: "+format+"\n", args...)
	}
}

// NewRoot builds the command tree against opts.
func NewRoot(opts *Options) *cobra.Command {
	root := &cobra.Command{
		Use:   "patchy",
		Short: "Work with patchy security findings from the terminal",
		Long: "patchy lists, inspects, reviews and acts on the custom resources that carry the\n" +
			"patchy pipeline's state machine.\n\n" +
			"It talks to the Kubernetes API with your own kubeconfig — never through a\n" +
			"controller or the status page — so what you can do is what your RBAC allows.\n" +
			"Run `patchy can-i` to see your grants.",
		Version:       fmt.Sprintf("%s (commit %s, built %s)", version.Version, version.Commit, version.BuildDate),
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	pf := root.PersistentFlags()
	pf.StringVar(&opts.Kubeconfig, "kubeconfig", "", "path to the kubeconfig file")
	pf.StringVar(&opts.Context, "context", "", "kubeconfig context to use")
	pf.StringVarP(&opts.Namespace, "namespace", "n", "", "namespace to work in (default: the context's)")
	pf.BoolVarP(&opts.AllNamespaces, "all-namespaces", "A", false, "work across every namespace")
	pf.StringVarP(&opts.Output, "output", "o", string(printer.FormatTable),
		"output format: table, wide, json, yaml, name, or markdown")
	pf.BoolVar(&opts.NoColor, "no-color", false, "disable colour and styling")
	pf.DurationVar(&opts.RequestTimeout, "request-timeout", defaultTimeout, "timeout for a single API call")
	pf.BoolVarP(&opts.Verbose, "verbose", "v", false, "log what the CLI is doing to stderr")

	_ = root.RegisterFlagCompletionFunc("output", fixedCompletion(printer.Formats()))
	_ = root.RegisterFlagCompletionFunc("namespace", noFileCompletion)

	root.AddCommand(
		newGetCmd(opts),
		newDescribeCmd(opts),
		newReviewCmd(opts),
		newBrowseCmd(opts),
		newCanICmd(opts),
	)
	root.AddCommand(newActionCmds(opts)...)
	return root
}

// Execute builds and runs the CLI, returning the process exit code. main is the
// only place allowed to call os.Exit.
func Execute() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	opts := &Options{Out: os.Stdout, ErrOut: os.Stderr}
	root := NewRoot(opts)
	root.SetOut(os.Stdout)
	root.SetErr(os.Stderr)
	// A flag or argument mistake is the caller's, not a failure: cobra reports
	// it through the same path as a runtime error, so tag it as usage here.
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return errUsage(err) })

	err := root.ExecuteContext(ctx)
	if err == nil {
		return ExitOK
	}
	fmt.Fprintln(os.Stderr, "error:", err)
	if isUsage(err) {
		fmt.Fprintln(os.Stderr, "\nRun 'patchy --help' for usage.")
	}
	return exitCode(err)
}

// fixedCompletion completes from a static list.
func fixedCompletion(values []string) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return values, cobra.ShellCompDirectiveNoFileComp
	}
}

// noFileCompletion suppresses filename completion where it would be noise.
func noFileCompletion(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return nil, cobra.ShellCompDirectiveNoFileComp
}

// nounCompletion completes the noun argument of a <verb> <noun> command.
func nounCompletion(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return resource.Spellings(), cobra.ShellCompDirectiveNoFileComp
}
