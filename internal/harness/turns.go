// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package harness

import (
	"encoding/json"
	"strings"
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
