// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package printer

import (
	"fmt"
	"io"
	"strings"
)

// Doc builds the detail views — what `describe` and `review` print.
//
// Commands declare structure (a section, a field, a body of markdown) and never
// formatting. The same declarations render as styled terminal output or as
// markdown depending only on the printer's format, which is why `patchy review
// -o markdown` pastes cleanly into an issue without a second code path.
type Doc struct {
	p        *Printer
	sections []section
}

type section struct {
	title  string
	fields []field
	bodies []string
}

type field struct {
	key   string
	value string
}

// Doc starts a document.
func (p *Printer) Doc() *Doc { return &Doc{p: p} }

// Section starts a new section. Sections with no content are dropped at render
// time, so a command can declare every section unconditionally and let absent
// data disappear on its own.
func (d *Doc) Section(title string) *Doc {
	d.sections = append(d.sections, section{title: title})
	return d
}

// Field adds a key/value line to the current section. An empty value is
// skipped: a detail view listing a dozen "<none>" rows buries the handful of
// fields that actually say something.
func (d *Doc) Field(key, value string) *Doc {
	if value == "" {
		return d
	}
	d.current().fields = append(d.current().fields, field{key: key, value: value})
	return d
}

// Fieldf adds a formatted field.
func (d *Doc) Fieldf(key, format string, args ...any) *Doc {
	return d.Field(key, fmt.Sprintf(format, args...))
}

// Body adds a block of markdown to the current section — an agent report, a
// summary paragraph.
func (d *Doc) Body(markdown string) *Doc {
	if strings.TrimSpace(markdown) == "" {
		return d
	}
	d.current().bodies = append(d.current().bodies, markdown)
	return d
}

// current returns the section being built, opening an untitled one if the
// caller adds a field before any Section call.
func (d *Doc) current() *section {
	if len(d.sections) == 0 {
		d.sections = append(d.sections, section{})
	}
	return &d.sections[len(d.sections)-1]
}

// Render writes the document.
func (d *Doc) Render() error {
	w := &errWriter{w: d.p.out}
	if d.p.format == FormatMarkdown {
		d.renderMarkdown(w)
	} else {
		d.renderTerm(w)
	}
	return w.err
}

// renderMarkdown emits the document as markdown source.
func (d *Doc) renderMarkdown(w *errWriter) {
	first := true
	for _, s := range d.sections {
		if s.empty() {
			continue
		}
		if !first {
			w.printf("\n")
		}
		first = false
		if s.title != "" {
			w.printf("## %s\n\n", s.title)
		}
		for _, f := range s.fields {
			w.printf("- **%s:** %s\n", f.key, f.value)
		}
		for _, b := range s.bodies {
			if len(s.fields) > 0 {
				w.printf("\n")
			}
			w.printf("%s\n", strings.TrimRight(b, "\n"))
		}
	}
}

// renderTerm emits the document for a human, aligning each section's keys
// independently so a section of short keys is not stretched by a distant long
// one.
func (d *Doc) renderTerm(w *errWriter) {
	first := true
	for _, s := range d.sections {
		if s.empty() {
			continue
		}
		if !first {
			w.printf("\n")
		}
		first = false
		if s.title != "" {
			w.printf("%s\n", paint(d.p.color, styleHeader, s.title))
		}

		width := 0
		for _, f := range s.fields {
			if len(f.key) > width {
				width = len(f.key)
			}
		}
		for _, f := range s.fields {
			key := f.key + ":" + strings.Repeat(" ", width-len(f.key))
			w.printf("  %s  %s\n", paint(d.p.color, styleKey, key), f.value)
		}

		for _, b := range s.bodies {
			if len(s.fields) > 0 {
				w.printf("\n")
			}
			w.printf("%s", d.p.renderMarkdownBody(b))
		}
	}
}

// errWriter collects the first write error so a long render reads as a
// sequence of prints rather than a chain of error checks. A failed stdout is
// not recoverable mid-document anyway; what matters is that Render reports it.
type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) printf(format string, args ...any) {
	if e.err != nil {
		return
	}
	_, e.err = fmt.Fprintf(e.w, format, args...)
}

// empty reports a section with nothing worth printing.
func (s section) empty() bool { return len(s.fields) == 0 && len(s.bodies) == 0 }
