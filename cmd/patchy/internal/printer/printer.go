// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package printer

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

// Printer writes command results in one format.
type Printer struct {
	out    io.Writer
	format Format
	color  bool
}

// New builds a Printer.
func New(out io.Writer, format Format, color bool) *Printer {
	// Markdown is for pasting elsewhere; styling it would paste escape codes.
	if format == FormatMarkdown {
		color = false
	}
	return &Printer{out: out, format: format, color: color}
}

// Format reports the printer's format, for commands that shape their own
// output (describe, review).
func (p *Printer) Format() Format { return p.format }

// Color reports whether styling is on.
func (p *Printer) Color() bool { return p.color }

// Out exposes the underlying writer for commands writing free-form text.
func (p *Printer) Out() io.Writer { return p.out }

// Table renders a server-rendered table. Columns the CRD marked
// priority>0 are held back unless the format is wide — the same rule kubectl
// applies, and the reason `-o wide` shows issue and PR links.
func (p *Printer) Table(t *metav1.Table) error {
	cols, rows := visible(t, p.format == FormatWide)
	if len(rows) == 0 {
		return p.empty()
	}
	if p.format == FormatMarkdown {
		return p.markdownTable(cols, rows)
	}
	if p.color {
		return p.styledTable(cols, rows)
	}
	return p.plainTable(cols, rows)
}

// visible selects the columns for the format and pulls each row's cells out as
// strings.
func visible(t *metav1.Table, wide bool) ([]string, [][]string) {
	var cols []string
	var keep []int
	for i, c := range t.ColumnDefinitions {
		if c.Priority != 0 && !wide {
			continue
		}
		cols = append(cols, strings.ToUpper(c.Name))
		keep = append(keep, i)
	}

	rows := make([][]string, 0, len(t.Rows))
	for _, r := range t.Rows {
		row := make([]string, 0, len(keep))
		for _, i := range keep {
			row = append(row, cell(r.Cells, i))
		}
		rows = append(rows, row)
	}
	return cols, rows
}

// cell stringifies one table cell. The API server sends JSON scalars, so
// numbers arrive as float64 and have to render without a decimal tail.
func cell(cells []any, i int) string {
	if i >= len(cells) || cells[i] == nil {
		return ""
	}
	switch v := cells[i].(type) {
	case string:
		return v
	case float64:
		if v == float64(int64(v)) {
			return fmt.Sprintf("%d", int64(v))
		}
		return fmt.Sprintf("%g", v)
	case bool:
		return fmt.Sprintf("%t", v)
	default:
		return fmt.Sprint(v)
	}
}

// plainTable pads with tabwriter. Correct only because nothing here is styled:
// tabwriter measures bytes, which ANSI escapes would inflate.
func (p *Printer) plainTable(cols []string, rows [][]string) error {
	tw := tabwriter.NewWriter(p.out, 0, 0, 3, ' ', 0)
	w := &errWriter{w: tw}
	w.printf("%s\n", strings.Join(cols, "\t"))
	for _, r := range rows {
		w.printf("%s\n", strings.Join(r, "\t"))
	}
	if w.err != nil {
		return w.err
	}
	return tw.Flush()
}

// styledTable lays out with lipgloss, which measures display width and so
// stays aligned with escape codes in the cells.
func (p *Printer) styledTable(cols []string, rows [][]string) error {
	t := table.New().
		Border(lipgloss.HiddenBorder()).
		BorderTop(false).BorderBottom(false).BorderLeft(false).BorderRight(false).
		BorderColumn(false).BorderRow(false).
		Headers(cols...).
		Rows(rows...).
		StyleFunc(func(row, col int) lipgloss.Style {
			// lipgloss numbers the header row -1.
			if row < 0 {
				return styleHeader.PaddingRight(2)
			}
			base := lipgloss.NewStyle().PaddingRight(2)
			if col < len(cols) && row < len(rows) {
				if s := styleFor(cols[col], rows[row][col]); s != nil {
					return s.PaddingRight(2)
				}
			}
			return base
		})
	w := &errWriter{w: p.out}
	w.printf("%s\n", t.Render())
	return w.err
}

// markdownTable emits a GitHub-flavoured table. Cell contents are escaped so a
// pipe inside a value cannot break the column structure.
func (p *Printer) markdownTable(cols []string, rows [][]string) error {
	w := &errWriter{w: p.out}
	w.printf("| %s |\n", strings.Join(cols, " | "))
	w.printf("|%s\n", strings.Repeat(" --- |", len(cols)))
	for _, r := range rows {
		escaped := make([]string, len(r))
		for i, c := range r {
			escaped[i] = strings.ReplaceAll(c, "|", `\|`)
		}
		w.printf("| %s |\n", strings.Join(escaped, " | "))
	}
	return w.err
}

// empty reports a result set with nothing in it. It goes to stderr so an empty
// `-o json` stays valid JSON and an empty table does not put a prose line into
// a pipe.
func (p *Printer) empty() error {
	return nil
}

// Objects emits whole objects for the machine-facing formats.
func (p *Printer) Objects(items []any, names []string) error {
	switch p.format {
	case FormatJSON:
		return p.writeJSON(items)
	case FormatYAML:
		return p.writeYAML(items)
	case FormatName:
		w := &errWriter{w: p.out}
		for _, n := range names {
			w.printf("%s\n", n)
		}
		return w.err
	default:
		return fmt.Errorf("format %s does not emit objects", p.format)
	}
}

// writeJSON emits a single object bare and several as a List, which is what
// kubectl does and what jq expressions assume.
func (p *Printer) writeJSON(items []any) error {
	enc := json.NewEncoder(p.out)
	enc.SetIndent("", "    ")
	if len(items) == 1 {
		return enc.Encode(items[0])
	}
	return enc.Encode(map[string]any{
		"apiVersion": "v1",
		"kind":       "List",
		"items":      items,
	})
}

// writeYAML emits objects as a multi-document stream.
func (p *Printer) writeYAML(items []any) error {
	w := &errWriter{w: p.out}
	for i, item := range items {
		if i > 0 {
			w.printf("---\n")
		}
		b, err := yaml.Marshal(item)
		if err != nil {
			return fmt.Errorf("encode yaml: %w", err)
		}
		if _, err := p.out.Write(b); err != nil {
			return err
		}
	}
	return w.err
}
