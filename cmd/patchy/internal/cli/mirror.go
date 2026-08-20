// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package cli

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/bitwise-media-group/patchy/internal/mirror"
	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
)

// newMirrorCmd groups the vendored chart/artifact mirroring verbs.
// Cluster-free like `dev`: everything works on a mirror store checkout and
// registries; the persistent kubeconfig flags are inert here.
func newMirrorCmd(opts *Options) *cobra.Command {
	f := &mirrorFlags{}
	cmd := &cobra.Command{
		Use:   "mirror",
		Short: "Mirror upstream helm charts and OCI artifacts into a platform registry",
		Long: "Maintain a vendored mirror store: a git tree where mirror.yaml holds global\n" +
			"defaults and every charts/<name>/ or artifacts/<name>/ directory pins one\n" +
			"upstream chart or OCI artifact — vendored for review, digest-locked,\n" +
			"provenance-verified, scanned, and published signed.\n\n" +
			"`upgrade` moves pins forward and regenerates the derived state; `sync`\n" +
			"converges the registry onto the committed state (idempotent, never replacing\n" +
			"an existing tag); `validate` proves the committed state is self-consistent\n" +
			"without touching the tree. patchy never runs git: commits, branches and PRs\n" +
			"belong to the calling pipeline.\n\n" +
			"Flags also resolve from PATCHY_MIRROR_* environment variables and an optional\n" +
			".patchy.yaml (mirror: block) in the working directory.",
		Args: cobra.NoArgs,
	}
	pf := cmd.PersistentFlags()
	pf.StringVarP(&f.directory, "directory", "C", "",
		"mirror store directory (walks up to mirror.yaml; default: the working directory)")
	pf.IntVar(&f.workers, "workers", 0, "concurrent registry operations per stage (default 4)")
	cmd.AddCommand(
		newMirrorAddCmd(opts, f),
		newMirrorUpgradeCmd(opts, f),
		newMirrorSyncCmd(opts, f),
		newMirrorValidateCmd(opts, f),
	)
	return cmd
}

// mirrorFlags carries the persistent mirror-group flags.
type mirrorFlags struct {
	directory string
	workers   int
}

// mirrorKeys maps the persistent flags onto the mirror: config block.
var mirrorKeys = map[string]string{
	"directory": "mirror.directory",
	"workers":   "mirror.workers",
}

// mirrorRoot resolves the store root: the explicit/config directory or the
// working directory, walked up to mirror.yaml.
func mirrorRoot(cmd *cobra.Command, opts *Options, f *mirrorFlags) (string, error) {
	v, err := loadCLIConfig(cmd, mirrorKeys)
	if err != nil {
		return "", err
	}
	noteConfigFile(opts, v)
	f.directory = v.GetString("mirror.directory")
	f.workers = v.GetInt("mirror.workers")
	dir := f.directory
	if dir == "" {
		dir = "."
	}
	root, err := spec.FindRoot(dir)
	if err != nil {
		return "", errUsage(err)
	}
	return root, nil
}

// mirrorEngine builds the engine for a resolved root, narrating events to
// stderr.
func mirrorEngine(opts *Options, f *mirrorFlags, root string) (*mirror.Engine, error) {
	global, err := spec.LoadConfig(filepath.Join(root, spec.ConfigFile))
	if err != nil {
		return nil, err
	}
	return mirror.New(mirror.Config{
		Root:    root,
		Global:  global,
		Workers: f.workers,
		OnEvent: func(e mirror.Event) { renderMirrorEvent(opts, e) },
	}), nil
}

// renderMirrorEvent narrates one engine event on stderr. stdout is
// reserved for the verb's data.
func renderMirrorEvent(opts *Options, e mirror.Event) {
	prefix := "patchy: "
	if e.Kind == "warn" {
		prefix = "patchy: warning: "
	}
	if e.Entry != "" {
		notef(opts.ErrOut, "%s[%s] %s\n", prefix, e.Entry, e.Message)
		return
	}
	notef(opts.ErrOut, "%s%s\n", prefix, e.Message)
}

// selectMirrorEntries resolves the entries a verb operates on: explicit
// names, a lockstep group, or the whole store.
func selectMirrorEntries(eng *mirror.Engine, names []string, all bool, group string) ([]spec.Entry, error) {
	switch {
	case all && (len(names) > 0 || group != ""):
		return nil, errUsage(fmt.Errorf("--all cannot be combined with entry names or --group"))
	case group != "" && len(names) > 0:
		return nil, errUsage(fmt.Errorf("--group cannot be combined with entry names"))
	case all:
		entries, err := eng.Entries()
		if err != nil {
			return nil, err
		}
		if len(entries) == 0 {
			return nil, fmt.Errorf("the store has no entries")
		}
		return entries, nil
	case group != "":
		entries, err := eng.Entries()
		if err != nil {
			return nil, err
		}
		var out []spec.Entry
		for _, e := range entries {
			if e.Lockstep() == group || (e.Lockstep() == "" && e.Name == group) {
				out = append(out, e)
			}
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("no entries in group %q", group)
		}
		return out, nil
	case len(names) > 0:
		var out []spec.Entry
		for _, name := range names {
			e, err := eng.Entry(name)
			if err != nil {
				return nil, err
			}
			out = append(out, e)
		}
		return out, nil
	default:
		return nil, errUsage(fmt.Errorf("name one or more entries, or pass --all"))
	}
}

// mirrorJSON writes one JSON document to stdout.
func mirrorJSON(opts *Options, v any) error {
	enc := json.NewEncoder(opts.Out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// mirrorTable writes an aligned table to stdout.
func mirrorTable(opts *Options, header []string, rows [][]string) error {
	w := tabwriter.NewWriter(opts.Out, 2, 8, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, tabJoin(header)); err != nil {
		return err
	}
	for _, row := range rows {
		if _, err := fmt.Fprintln(w, tabJoin(row)); err != nil {
			return err
		}
	}
	return w.Flush()
}

// tabJoin joins cells for the tabwriter.
func tabJoin(cells []string) string {
	var out strings.Builder
	for i, c := range cells {
		if i > 0 {
			out.WriteString("\t")
		}
		out.WriteString(c)
	}
	return out.String()
}

// markdownBuilder assembles the -o markdown summaries: H3 sections per
// entry (so a caller can put its own H2 above the whole thing), blank
// lines where markdown needs them, fenced blocks for machine detail.
type markdownBuilder struct {
	b strings.Builder
}

// Section starts an entry section.
func (m *markdownBuilder) Section(name string) {
	if m.b.Len() > 0 {
		m.b.WriteString("\n")
	}
	fmt.Fprintf(&m.b, "### `%s`\n\n", name)
}

// Linef appends one paragraph line.
func (m *markdownBuilder) Linef(format string, args ...any) {
	fmt.Fprintf(&m.b, format+"\n", args...)
}

// Fenced appends a labeled fenced block, one item per line.
func (m *markdownBuilder) Fenced(label string, items []string) {
	fmt.Fprintf(&m.b, "%s\n\n```\n%s\n```\n", label, strings.Join(items, "\n"))
}

// String returns the assembled document.
func (m *markdownBuilder) String() string { return m.b.String() }
