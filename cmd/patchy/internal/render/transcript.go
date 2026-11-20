// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package render

import (
	"fmt"
	"io"
	"strings"

	"github.com/bitwise-media-group/patchy/internal/transcript"
)

// Transcript writes an agent conversation as plain text: one header line per
// turn (sequence, time, who, and the tool for a tool call), then the turn's
// text indented beneath it. It is for reading in a pager; --raw gives the
// JSONL instead.
func Transcript(w io.Writer, turns []transcript.Turn) error {
	for _, t := range turns {
		head := fmt.Sprintf("#%d", t.Seq)
		if t.At != "" {
			head += " " + t.At
		}
		head += " " + turnLabel(t)
		if t.Tool != "" {
			head += " " + t.Tool
		}
		if t.Path != "" {
			head += " " + t.Path
		}
		if t.Truncated {
			head += " (truncated)"
		}
		if _, err := fmt.Fprintln(w, head); err != nil {
			return err
		}
		if text := strings.TrimRight(t.Text, "\n"); text != "" {
			for line := range strings.SplitSeq(text, "\n") {
				if _, err := fmt.Fprintln(w, "    "+line); err != nil {
					return err
				}
			}
		}
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
	}
	return nil
}

// turnLabel names a turn's kind in the reader's terms, as the status page does.
func turnLabel(t transcript.Turn) string {
	switch t.Kind {
	case transcript.KindText:
		return string(t.Role)
	case transcript.KindToolUse:
		return "tool"
	case transcript.KindToolResult:
		return "result"
	case transcript.KindNotice:
		return "session"
	default:
		return string(t.Kind)
	}
}
