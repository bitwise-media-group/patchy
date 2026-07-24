// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package printer

import (
	"bytes"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// testTable mirrors the shape the API server returns for findings, including a
// priority>0 column (the CRD marks the issue link that way).
func testTable() *metav1.Table {
	return &metav1.Table{
		ColumnDefinitions: []metav1.TableColumnDefinition{
			{Name: "Name", Type: "string"},
			{Name: "Severity", Type: "string"},
			{Name: "Phase", Type: "string"},
			{Name: "Issue", Type: "string", Priority: 1},
		},
		Rows: []metav1.TableRow{
			{Cells: []any{"fnd-1", "critical", "Queued", "https://example.test/1"}},
			{Cells: []any{"fnd-2", "low", "Remediated", "https://example.test/2"}},
		},
	}
}

func TestTableFormats(t *testing.T) {
	cases := []struct {
		name    string
		format  Format
		want    []string
		exclude []string
	}{
		{
			name:    "table hides the low-priority columns",
			format:  FormatTable,
			want:    []string{"NAME", "SEVERITY", "PHASE", "fnd-1", "critical"},
			exclude: []string{"ISSUE", "https://example.test/1"},
		},
		{
			name:   "wide includes them",
			format: FormatWide,
			want:   []string{"ISSUE", "https://example.test/1"},
		},
		{
			name:   "markdown is a pipe table",
			format: FormatMarkdown,
			want: []string{
				"| NAME | SEVERITY | PHASE |",
				"| --- | --- | --- |",
				"| fnd-1 | critical | Queued |",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			p := New(&buf, tc.format, false)
			if err := p.Table(testTable()); err != nil {
				t.Fatalf("Table: %v", err)
			}
			got := buf.String()
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("output missing %q:\n%s", w, got)
				}
			}
			for _, x := range tc.exclude {
				if strings.Contains(got, x) {
					t.Errorf("output should not contain %q:\n%s", x, got)
				}
			}
		})
	}
}

// TestPlainTableHasNoEscapes is what makes the piped output safe to parse: a
// stray escape sequence would land in the middle of an awk field.
func TestPlainTableHasNoEscapes(t *testing.T) {
	var buf bytes.Buffer
	p := New(&buf, FormatTable, false)
	if err := p.Table(testTable()); err != nil {
		t.Fatalf("Table: %v", err)
	}
	if strings.Contains(buf.String(), "\x1b") {
		t.Errorf("unstyled output contains ANSI escapes:\n%q", buf.String())
	}
}

// TestMarkdownEscapesPipes guards the column structure against values that
// contain the delimiter.
func TestMarkdownEscapesPipes(t *testing.T) {
	tbl := &metav1.Table{
		ColumnDefinitions: []metav1.TableColumnDefinition{{Name: "Title", Type: "string"}},
		Rows:              []metav1.TableRow{{Cells: []any{"a | b"}}},
	}
	var buf bytes.Buffer
	if err := New(&buf, FormatMarkdown, false).Table(tbl); err != nil {
		t.Fatalf("Table: %v", err)
	}
	if !strings.Contains(buf.String(), `a \| b`) {
		t.Errorf("pipe not escaped:\n%s", buf.String())
	}
}

// TestCellTypes covers the JSON scalars a table can carry: an integer column
// rendered as "3.000000" would be a visible defect.
func TestCellTypes(t *testing.T) {
	tbl := &metav1.Table{
		ColumnDefinitions: []metav1.TableColumnDefinition{
			{Name: "Attempt", Type: "integer"},
			{Name: "Success", Type: "boolean"},
			{Name: "Missing", Type: "string"},
		},
		Rows: []metav1.TableRow{{Cells: []any{float64(3), true, nil}}},
	}
	var buf bytes.Buffer
	if err := New(&buf, FormatTable, false).Table(tbl); err != nil {
		t.Fatalf("Table: %v", err)
	}
	got := buf.String()
	for _, want := range []string{"3", "true"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "3.0") {
		t.Errorf("integer rendered as a float:\n%s", got)
	}
}

// TestShortRowDoesNotPanic: the server can send fewer cells than columns.
func TestShortRowDoesNotPanic(t *testing.T) {
	tbl := &metav1.Table{
		ColumnDefinitions: []metav1.TableColumnDefinition{
			{Name: "Name", Type: "string"},
			{Name: "Phase", Type: "string"},
		},
		Rows: []metav1.TableRow{{Cells: []any{"fnd-1"}}},
	}
	var buf bytes.Buffer
	if err := New(&buf, FormatTable, false).Table(tbl); err != nil {
		t.Fatalf("Table: %v", err)
	}
}

func TestParseFormat(t *testing.T) {
	for _, in := range []string{"table", "WIDE", " json ", "markdown"} {
		if _, err := ParseFormat(in); err != nil {
			t.Errorf("ParseFormat(%q): %v", in, err)
		}
	}
	if _, err := ParseFormat("xml"); err == nil {
		t.Error("ParseFormat accepted an unknown format")
	}
}

func TestFormatStructured(t *testing.T) {
	for _, f := range []Format{FormatJSON, FormatYAML, FormatName} {
		if !f.Structured() {
			t.Errorf("%s should be structured", f)
		}
	}
	for _, f := range []Format{FormatTable, FormatWide, FormatMarkdown} {
		if f.Structured() {
			t.Errorf("%s should not be structured", f)
		}
	}
}

// TestMarkdownFormatIsNeverStyled: -o markdown exists to be pasted elsewhere,
// so escape codes must not survive even on a terminal.
func TestMarkdownFormatIsNeverStyled(t *testing.T) {
	var buf bytes.Buffer
	if p := New(&buf, FormatMarkdown, true); p.Color() {
		t.Error("markdown output is styled")
	}
}

func TestObjects(t *testing.T) {
	type obj struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
	}
	one := obj{Kind: "Finding", Name: "fnd-1"}
	two := obj{Kind: "Finding", Name: "fnd-2"}

	cases := []struct {
		name    string
		format  Format
		items   []any
		want    []string
		exclude []string
	}{
		{
			name:    "a single object is emitted bare",
			format:  FormatJSON,
			items:   []any{one},
			want:    []string{`"name": "fnd-1"`},
			exclude: []string{`"kind": "List"`},
		},
		{
			name:   "several objects become a List",
			format: FormatJSON,
			items:  []any{one, two},
			want:   []string{`"kind": "List"`, "fnd-1", "fnd-2"},
		},
		{
			name:   "yaml is a document stream",
			format: FormatYAML,
			items:  []any{one, two},
			want:   []string{"name: fnd-1", "---", "name: fnd-2"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := New(&buf, tc.format, false).Objects(tc.items, nil); err != nil {
				t.Fatalf("Objects: %v", err)
			}
			got := buf.String()
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("output missing %q:\n%s", w, got)
				}
			}
			for _, x := range tc.exclude {
				if strings.Contains(got, x) {
					t.Errorf("output should not contain %q:\n%s", x, got)
				}
			}
		})
	}
}

func TestDoc(t *testing.T) {
	build := func(p *Printer) error {
		return p.Doc().
			Section("Finding fnd-1").
			Field("Phase", "Queued").
			Field("Empty", "").
			Section("Report").
			Body("# Verdict\n\nRemediate.").
			Section("Nothing here").
			Render()
	}

	t.Run("markdown", func(t *testing.T) {
		var buf bytes.Buffer
		if err := build(New(&buf, FormatMarkdown, false)); err != nil {
			t.Fatalf("Render: %v", err)
		}
		got := buf.String()
		for _, want := range []string{"## Finding fnd-1", "- **Phase:** Queued", "# Verdict"} {
			if !strings.Contains(got, want) {
				t.Errorf("output missing %q:\n%s", want, got)
			}
		}
		if strings.Contains(got, "Empty") {
			t.Errorf("empty field rendered:\n%s", got)
		}
		if strings.Contains(got, "Nothing here") {
			t.Errorf("empty section rendered:\n%s", got)
		}
	})

	t.Run("terminal", func(t *testing.T) {
		var buf bytes.Buffer
		if err := build(New(&buf, FormatTable, false)); err != nil {
			t.Fatalf("Render: %v", err)
		}
		got := buf.String()
		if !strings.Contains(got, "Phase:") || !strings.Contains(got, "Queued") {
			t.Errorf("field missing:\n%s", got)
		}
		if strings.Contains(got, "\x1b") {
			t.Errorf("unstyled document contains escapes:\n%q", got)
		}
	})
}

// TestColor covers the opt-outs. A bytes.Buffer is never a terminal, which is
// also what makes every other test in this file deterministic.
func TestColor(t *testing.T) {
	var buf bytes.Buffer
	if Color(&buf, false) {
		t.Error("styled a non-terminal writer")
	}
	if Color(&buf, true) {
		t.Error("styled despite --no-color")
	}
	t.Setenv("NO_COLOR", "1")
	if Color(&buf, false) {
		t.Error("styled despite NO_COLOR")
	}
}

// TestStyledTableEmitsStyling is the counterpart to TestPlainTableHasNoEscapes,
// and exists because a styling library upgrade can silently stop styling
// without failing a single content assertion: every other test here asserts
// text, which unstyled output still satisfies.
func TestStyledTableEmitsStyling(t *testing.T) {
	var buf bytes.Buffer
	p := New(&buf, FormatTable, true)
	if err := p.Table(testTable()); err != nil {
		t.Fatalf("Table: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "\x1b") {
		t.Errorf("styled table emitted no ANSI escapes:\n%q", got)
	}
	// Alignment has to survive the escapes: lipgloss measures display width,
	// which is the whole reason the styled path does not use tabwriter.
	for _, want := range []string{"NAME", "fnd-1", "critical"} {
		if !strings.Contains(got, want) {
			t.Errorf("styled table lost %q:\n%q", want, got)
		}
	}
}

// TestStyledMarkdownRenders covers the glamour path the same way.
func TestStyledMarkdownRenders(t *testing.T) {
	var buf bytes.Buffer
	p := New(&buf, FormatTable, true)
	if err := p.Markdown("# Verdict\n\nRemediate: the input is **unsanitised**."); err != nil {
		t.Fatalf("Markdown: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "Verdict") || !strings.Contains(got, "unsanitised") {
		t.Errorf("rendered markdown lost its content:\n%q", got)
	}
	// The renderer must not leak markdown syntax into rendered output.
	if strings.Contains(got, "**") {
		t.Errorf("markdown was not rendered, only passed through:\n%q", got)
	}
}
