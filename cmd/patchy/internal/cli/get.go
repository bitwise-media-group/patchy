// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/kubecfg"
	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/printer"
	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/resource"
	"github.com/bitwise-media-group/patchy/internal/action"
)

// getFlags are the filters `get` accepts. Some resolve to label selectors and
// run on the API server; the rest need the object and run here.
type getFlags struct {
	selector  string
	severity  []string
	source    string
	finding   string
	phase     []string
	priority  []string
	verdict   []string
	repo      string
	suspended bool
	awaiting  bool
	sortBy    string
}

// findingOnly reports whether a filter that only makes sense for findings was
// set, so using one against another noun can be refused rather than ignored.
func (f *getFlags) findingOnly() []string {
	var used []string
	if len(f.phase) > 0 {
		used = append(used, "--phase")
	}
	if len(f.priority) > 0 {
		used = append(used, "--priority")
	}
	if len(f.verdict) > 0 {
		used = append(used, "--verdict")
	}
	if f.repo != "" {
		used = append(used, "--repo")
	}
	if f.suspended {
		used = append(used, "--suspended")
	}
	if f.awaiting {
		used = append(used, "--awaiting")
	}
	return used
}

// needsObjects reports whether a filter requires reading the objects rather
// than just the rendered rows.
func (f *getFlags) needsObjects() bool { return len(f.findingOnly()) > 0 }

func newGetCmd(opts *Options) *cobra.Command {
	f := &getFlags{}
	cmd := &cobra.Command{
		Use:   "get <resource> [name...]",
		Short: "List patchy resources",
		Long: "List patchy resources.\n\n" +
			"Columns come from the CRDs themselves, so they match `kubectl get` exactly;\n" +
			"-o wide adds the columns marked lower priority (issue and pull-request links).",
		Example: "  patchy get findings\n" +
			"  patchy get findings --phase AwaitingApproval --severity critical\n" +
			"  patchy get findings --awaiting -o wide\n" +
			"  patchy get investigations --finding my-finding\n" +
			"  patchy get fnd my-finding -o yaml",
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: nounCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGet(cmd.Context(), opts, f, args[0], args[1:])
		},
	}

	fl := cmd.Flags()
	fl.StringVarP(&f.selector, "selector", "l", "", "label selector (server-side)")
	fl.StringSliceVar(&f.severity, "severity", nil, "only these severities: low, medium, high, critical")
	fl.StringVar(&f.source, "source", "", "only findings from this source handler")
	fl.StringVar(&f.finding, "finding", "", "only runs belonging to this finding")
	fl.StringSliceVar(&f.phase, "phase", nil, "only findings in these phases")
	fl.StringSliceVar(&f.priority, "priority", nil, "only findings at these priorities")
	fl.StringSliceVar(&f.verdict, "verdict", nil, "only findings with these verdicts: remediate, ignore, manual")
	fl.StringVar(&f.repo, "repo", "", "only findings whose repository name contains this")
	fl.BoolVar(&f.suspended, "suspended", false, "only suspended findings")
	fl.BoolVar(&f.awaiting, "awaiting", false, "only findings with an action available")
	fl.StringVar(&f.sortBy, "sort-by", "age", "sort by: age, name, severity, priority, or phase")

	_ = cmd.RegisterFlagCompletionFunc("sort-by", fixedCompletion(sortKeys))
	_ = cmd.RegisterFlagCompletionFunc("phase", fixedCompletion(phaseNames()))
	_ = cmd.RegisterFlagCompletionFunc("severity", fixedCompletion(levelNames()))
	_ = cmd.RegisterFlagCompletionFunc("priority", fixedCompletion(levelNames()))
	_ = cmd.RegisterFlagCompletionFunc("verdict", fixedCompletion([]string{"remediate", "ignore", "manual"}))
	return cmd
}

func runGet(ctx context.Context, opts *Options, f *getFlags, noun string, names []string) error {
	kind, err := resource.Lookup(noun)
	if err != nil {
		return errUsage(err)
	}
	if used := f.findingOnly(); len(used) > 0 && kind.Singular != "finding" {
		return errUsage(fmt.Errorf("%s only applies to findings, not %s",
			strings.Join(used, "/"), kind.Plural))
	}
	if f.finding != "" && !kind.Run() {
		return errUsage(fmt.Errorf("--finding only applies to investigations and remediations, not %s", kind.Plural))
	}

	env, err := opts.Connect()
	if err != nil {
		return err
	}
	p, err := opts.Printer()
	if err != nil {
		return err
	}
	selector, err := buildSelector(f)
	if err != nil {
		return err
	}
	opts.debugf("listing %s in %s (selector %q)", kind.Plural, env.Scope(), selector)

	// The machine-facing formats want the objects themselves; nothing about a
	// rendered table would survive `| jq` anyway.
	if p.Format().Structured() {
		return getObjects(ctx, opts, env, p, kind, f, names, selector)
	}

	callCtx, cancel := opts.Timeout(ctx)
	defer cancel()
	table, err := env.Table(callCtx, kind.Plural, names, selector)
	if err != nil {
		return err
	}

	keep, err := survivingNames(ctx, opts, env, f, selector)
	if err != nil {
		return err
	}
	table.Rows = filterRows(table.Rows, names, keep)
	sortRows(table, f.sortBy)

	if len(table.Rows) == 0 {
		notef(opts.ErrOut, "No %s found in %s.\n", kind.Plural, env.Scope())
		return nil
	}
	return p.Table(table)
}

// survivingNames applies the object-level filters and returns the names that
// pass, or nil when no such filter was set (meaning "keep everything").
func survivingNames(ctx context.Context, opts *Options, env *kubecfg.Env,
	f *getFlags, selector string,
) (map[string]bool, error) {
	if !f.needsObjects() {
		return nil, nil
	}
	callCtx, cancel := opts.Timeout(ctx)
	defer cancel()

	var list v1alpha1.FindingList
	if err := env.Client.List(callCtx, &list, listOptions(env, selector)...); err != nil {
		return nil, err
	}
	keep := map[string]bool{}
	for i := range list.Items {
		if matchesFinding(&list.Items[i], f) {
			keep[list.Items[i].Name] = true
		}
	}
	opts.debugf("object filters kept %d/%d findings", len(keep), len(list.Items))
	return keep, nil
}

// getObjects emits whole objects for -o json/yaml/name.
func getObjects(ctx context.Context, opts *Options, env *kubecfg.Env, p *printer.Printer,
	kind resource.Kind, f *getFlags, names []string, selector string,
) error {
	callCtx, cancel := opts.Timeout(ctx)
	defer cancel()

	objs, err := fetchObjects(callCtx, env, kind, names, selector)
	if err != nil {
		return err
	}
	if f.needsObjects() {
		kept := objs[:0]
		for _, o := range objs {
			// Non-findings cannot be filtered by these flags and cannot reach
			// here anyway — runGet refuses the combination up front.
			if fnd, ok := o.(*v1alpha1.Finding); !ok || matchesFinding(fnd, f) {
				kept = append(kept, o)
			}
		}
		objs = kept
	}
	if len(objs) == 0 {
		notef(opts.ErrOut, "No %s found in %s.\n", kind.Plural, env.Scope())
		return nil
	}

	items := make([]any, 0, len(objs))
	refs := make([]string, 0, len(objs))
	for _, o := range objs {
		items = append(items, o)
		refs = append(refs, fmt.Sprintf("%s.%s/%s", kind.Plural, v1alpha1.GroupVersion.Group, o.GetName()))
	}
	return p.Objects(items, refs)
}

// fetchObjects reads either the named objects or the whole selected list.
func fetchObjects(ctx context.Context, env *kubecfg.Env, kind resource.Kind,
	names []string, selector string,
) ([]client.Object, error) {
	if len(names) > 0 {
		out := make([]client.Object, 0, len(names))
		for _, name := range names {
			obj := kind.New()
			if err := env.Client.Get(ctx, objectKey(env, name), obj); err != nil {
				return nil, err
			}
			out = append(out, obj)
		}
		return out, nil
	}

	list := kind.NewList()
	if err := env.Client.List(ctx, list, listOptions(env, selector)...); err != nil {
		return nil, err
	}
	return extractItems(list), nil
}

// matchesFinding applies the object-level filters to one finding.
func matchesFinding(fnd *v1alpha1.Finding, f *getFlags) bool {
	if len(f.phase) > 0 && !containsFold(f.phase, string(fnd.Status.Phase)) {
		return false
	}
	if len(f.priority) > 0 && !containsFold(f.priority, string(fnd.Status.Priority)) {
		return false
	}
	if len(f.verdict) > 0 {
		verdict := ""
		if inv := fnd.Status.Investigation; inv != nil {
			verdict = string(inv.Recommendation)
		}
		if !containsFold(f.verdict, verdict) {
			return false
		}
	}
	if f.repo != "" {
		name := ""
		if r := fnd.Spec.Repository; r != nil {
			name = r.Name
		}
		if !strings.Contains(strings.ToLower(name), strings.ToLower(f.repo)) {
			return false
		}
	}
	if f.suspended && !fnd.Spec.Suspend {
		return false
	}
	if f.awaiting && len(action.Available(fnd, now())) == 0 {
		return false
	}
	return true
}

// buildSelector folds the server-side filters into one label selector. These
// are the filters the API server can answer, so they never reach the client.
func buildSelector(f *getFlags) (string, error) {
	var terms []string
	if f.selector != "" {
		terms = append(terms, f.selector)
	}
	if len(f.severity) > 0 {
		for _, s := range f.severity {
			if !containsFold(levelNames(), s) {
				return "", errUsage(fmt.Errorf("unknown severity %q; want one of: %s",
					s, strings.Join(levelNames(), ", ")))
			}
		}
		terms = append(terms, fmt.Sprintf("%s in (%s)", v1alpha1.LabelSeverity,
			strings.ToLower(strings.Join(f.severity, ","))))
	}
	if f.source != "" {
		terms = append(terms, fmt.Sprintf("%s=%s", v1alpha1.LabelSource, f.source))
	}
	if f.finding != "" {
		terms = append(terms, fmt.Sprintf("%s=%s", v1alpha1.LabelFinding, f.finding))
	}
	return strings.Join(terms, ","), nil
}

// filterRows narrows rows to the explicitly named objects and to the survivors
// of the object-level filters. A nil keep set means no such filter ran.
func filterRows(rows []metav1.TableRow, names []string, keep map[string]bool) []metav1.TableRow {
	if len(names) == 0 && keep == nil {
		return rows
	}
	out := rows[:0]
	for _, row := range rows {
		name := kubecfg.RowName(row)
		if len(names) > 0 && !slices.Contains(names, name) {
			continue
		}
		if keep != nil && !keep[name] {
			continue
		}
		out = append(out, row)
	}
	return out
}

// sortKeys are the accepted --sort-by values.
var sortKeys = []string{"age", "name", "severity", "priority", "phase"}

// rank orders the severity/priority vocabulary by seriousness rather than
// alphabetically, so `--sort-by severity` puts critical first.
var rank = map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3, "none": 4, "": 5}

// sortRows orders the table. Ties fall back to name so repeated runs of the
// same command print the same order.
func sortRows(t *metav1.Table, key string) {
	col := func(name string) int {
		for i, c := range t.ColumnDefinitions {
			if strings.EqualFold(c.Name, name) {
				return i
			}
		}
		return -1
	}
	byCell := func(name string, ranked bool) {
		i := col(name)
		if i < 0 {
			return
		}
		sort.SliceStable(t.Rows, func(a, b int) bool {
			x, y := fmt.Sprint(cellAt(t.Rows[a], i)), fmt.Sprint(cellAt(t.Rows[b], i))
			if ranked {
				if rank[strings.ToLower(x)] != rank[strings.ToLower(y)] {
					return rank[strings.ToLower(x)] < rank[strings.ToLower(y)]
				}
			} else if x != y {
				return x < y
			}
			return kubecfg.RowName(t.Rows[a]) < kubecfg.RowName(t.Rows[b])
		})
	}

	switch strings.ToLower(key) {
	case "name":
		sort.SliceStable(t.Rows, func(a, b int) bool {
			return kubecfg.RowName(t.Rows[a]) < kubecfg.RowName(t.Rows[b])
		})
	case "severity":
		byCell("Severity", true)
	case "priority":
		byCell("Priority", true)
	case "phase":
		byCell("Phase", false)
	default: // age — newest first, which is what a triage session wants
		sort.SliceStable(t.Rows, func(a, b int) bool {
			ma, mb := kubecfg.RowMeta(t.Rows[a]), kubecfg.RowMeta(t.Rows[b])
			if ma == nil || mb == nil {
				return false
			}
			if !ma.CreationTimestamp.Equal(&mb.CreationTimestamp) {
				return mb.CreationTimestamp.Before(&ma.CreationTimestamp)
			}
			return ma.Name < mb.Name
		})
	}
}

// cellAt reads one cell defensively; a short row is not worth a panic.
func cellAt(row metav1.TableRow, i int) any {
	if i < len(row.Cells) {
		return row.Cells[i]
	}
	return ""
}
