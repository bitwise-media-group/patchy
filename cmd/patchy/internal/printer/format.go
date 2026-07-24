// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package printer

import (
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// Format is an output shape.
type Format string

// The output formats. table and wide differ only in whether the CRD's
// lower-priority print columns are included.
const (
	// FormatTable is the default: the server-rendered print columns.
	FormatTable Format = "table"
	// FormatWide adds the CRD's priority>0 columns.
	FormatWide Format = "wide"
	// FormatJSON emits the objects verbatim.
	FormatJSON Format = "json"
	// FormatYAML emits the objects verbatim.
	FormatYAML Format = "yaml"
	// FormatName emits `<resource>.<group>/<name>` lines, for xargs.
	FormatName Format = "name"
	// FormatMarkdown emits markdown — a table for lists, prose for reports.
	// This is the paste-into-a-ticket format, so it is never styled.
	FormatMarkdown Format = "markdown"
)

// allFormats is the accepted set, in help order.
var allFormats = []Format{FormatTable, FormatWide, FormatJSON, FormatYAML, FormatName, FormatMarkdown}

// ParseFormat resolves the -o value.
func ParseFormat(s string) (Format, error) {
	got := Format(strings.ToLower(strings.TrimSpace(s)))
	for _, f := range allFormats {
		if got == f {
			return f, nil
		}
	}
	return "", fmt.Errorf("unknown output format %q; want one of: %s", s, strings.Join(Formats(), ", "))
}

// Formats lists the accepted values, for help and completion.
func Formats() []string {
	out := make([]string, 0, len(allFormats))
	for _, f := range allFormats {
		out = append(out, string(f))
	}
	return out
}

// Structured reports whether the format is machine-facing, in which case
// commands emit whole objects rather than a human summary.
func (f Format) Structured() bool {
	return f == FormatJSON || f == FormatYAML || f == FormatName
}

// Color reports whether output to w should be styled.
//
// Styling is opt-out in three independent ways because each covers a case the
// others miss: --no-color is the explicit ask, NO_COLOR is the cross-tool
// convention, and a non-terminal writer means nobody is reading this with
// their eyes. TERM=dumb catches terminals that cannot render it.
func Color(w io.Writer, noColor bool) bool {
	if noColor || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}
