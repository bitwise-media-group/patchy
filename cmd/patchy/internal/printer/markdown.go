// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package printer

import (
	"os"
	"strings"

	"charm.land/glamour/v2"
	"golang.org/x/term"
)

// Wrapping bounds for rendered markdown. Agent reports contain long prose, and
// a report wrapped to a 200-column terminal is a wall nobody reads; 100 is
// about a comfortable line.
const (
	maxWrap      = 100
	minWrap      = 40
	fallbackWrap = 80
)

// Markdown writes a markdown document: rendered for a terminal, verbatim
// otherwise. "Otherwise" covers both a pipe and -o markdown, which is the
// point — the bytes a user pastes into an issue are the bytes the agent wrote.
func (p *Printer) Markdown(md string) error {
	_, err := p.out.Write([]byte(p.renderMarkdownBody(md)))
	return err
}

// renderMarkdownBody renders one block, falling back to the source text if the
// renderer cannot be built or fails. A styling failure must never cost the user
// the report itself — unrendered markdown is still perfectly readable, which is
// why this returns no error.
func (p *Printer) renderMarkdownBody(md string) string {
	body := strings.TrimRight(md, "\n") + "\n"
	if !p.color {
		return body
	}
	// glamour v2 dropped WithAutoStyle: lipgloss v2 no longer probes the
	// terminal's background, because doing so needs a query/response round trip
	// that only a TUI event loop can service. WithEnvironmentConfig honours
	// GLAMOUR_STYLE — the same variable glow and friends read, so a user who has
	// set it once is already configured — and falls back to the dark theme.
	// Users on a light terminal set GLAMOUR_STYLE=light; docs/cli.md says so.
	r, err := glamour.NewTermRenderer(
		glamour.WithEnvironmentConfig(),
		glamour.WithWordWrap(wrapWidth(p.out)),
	)
	if err != nil {
		return body
	}
	rendered, err := r.Render(body)
	if err != nil {
		return body
	}
	return rendered
}

// wrapWidth picks a wrap column from the terminal, clamped to something
// readable.
func wrapWidth(out any) int {
	f, ok := out.(*os.File)
	if !ok {
		return fallbackWrap
	}
	w, _, err := term.GetSize(int(f.Fd()))
	if err != nil || w < minWrap {
		return fallbackWrap
	}
	if w > maxWrap {
		return maxWrap
	}
	return w
}
