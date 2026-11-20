// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package harness

import (
	"encoding/json"
	"strings"

	"github.com/bitwise-media-group/patchy/internal/transcript"
)

// toolInputKeys are the argument names agent CLIs use for the one value that
// identifies what a tool call is doing, in the order they are preferred. A
// tool call rendered as "go test ./..." reads as a transcript; the same call
// rendered as its whole JSON argument object does not.
var toolInputKeys = []string{
	"command", "file_path", "path", "pattern", "query", "url", "prompt", "description", "content",
}

// renderToolInput projects a tool call's JSON arguments onto one human line:
// the identifying argument when there is one, the compact object otherwise.
// Callers cap the length; this only chooses what to show.
func renderToolInput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var args map[string]any
	if json.Unmarshal(raw, &args) != nil {
		return strings.TrimSpace(string(raw))
	}
	for _, key := range toolInputKeys {
		if v, ok := args[key]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	compact, err := json.Marshal(args)
	if err != nil {
		return strings.TrimSpace(string(raw))
	}
	return string(compact)
}

// renderToolCall renders a tool call for the transcript: the text a reader
// sees, the file it targets, and how to render the text. File-writing calls
// keep their body (an Edit is a diff, a Write is the content) because a
// remediation transcript that names a file but omits the change is not an
// account of the change. Bash keeps its command as shell. Anything else falls
// back to renderToolInput's one-argument projection.
//
// A Read renders as exactly its path, so a reader can tell a call that only
// names a file (and whose result is that file) from one carrying content.
// The recorder caps the text; this only chooses what to show.
func renderToolCall(name string, raw json.RawMessage) (text, path string, syntax transcript.Syntax) {
	var args struct {
		FilePath  string  `json:"file_path"`
		Command   string  `json:"command"`
		Content   *string `json:"content"`
		OldString string  `json:"old_string"`
		NewString string  `json:"new_string"`
		Edits     []struct {
			OldString string `json:"old_string"`
			NewString string `json:"new_string"`
		} `json:"edits"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &args) != nil {
		return renderToolInput(raw), "", ""
	}
	path = strings.TrimSpace(args.FilePath)
	switch name {
	case "Edit":
		if path != "" {
			return editDiff(args.OldString, args.NewString), path, transcript.SyntaxDiff
		}
	case "MultiEdit":
		if path != "" && len(args.Edits) > 0 {
			hunks := make([]string, 0, len(args.Edits))
			for _, e := range args.Edits {
				hunks = append(hunks, editDiff(e.OldString, e.NewString))
			}
			return strings.Join(hunks, "\n@@\n"), path, transcript.SyntaxDiff
		}
	case "Write":
		if path != "" && args.Content != nil {
			return *args.Content, path, ""
		}
	case "Read":
		if path != "" {
			return path, path, ""
		}
	case "Bash":
		if cmd := strings.TrimSpace(args.Command); cmd != "" {
			return cmd, "", transcript.SyntaxShell
		}
	}
	return renderToolInput(raw), "", ""
}

// editDiff renders a string replacement as a unified-diff-style hunk body:
// every old line removed, every new line added. It is a display, not a patch:
// the edit tools carry no line numbers or context to anchor one.
func editDiff(before, after string) string {
	var b strings.Builder
	for _, l := range diffLines(before) {
		b.WriteString("-" + l + "\n")
	}
	for _, l := range diffLines(after) {
		b.WriteString("+" + l + "\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// diffLines splits s into lines, with an empty string contributing none.
func diffLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// renderToolResult flattens a tool result's content, which the CLIs report
// either as a bare string or as an array of typed blocks.
func renderToolResult(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		var parts []string
		for _, b := range blocks {
			if strings.TrimSpace(b.Text) != "" {
				parts = append(parts, b.Text)
			}
		}
		if len(parts) > 0 {
			return strings.TrimSpace(strings.Join(parts, "\n"))
		}
	}
	return strings.TrimSpace(string(raw))
}
