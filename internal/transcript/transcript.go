// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package transcript

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Prefix marks a turn line on the agent-runner's stdout. It differs from
// envelope.Prefix so the controller's result path, which scans for stage
// results, never has to decode a run's few hundred turns.
const Prefix = "PATCHY-TURN: "

// Version is the current turn schema version.
const Version = 1

// Role is who produced a turn.
type Role string

// The roles. Tool results are reported by the harness as user-role turns,
// matching how the model sees them.
const (
	RoleAssistant Role = "assistant"
	RoleUser      Role = "user"
	RoleSystem    Role = "system"
)

// Kind discriminates what a turn carries.
type Kind string

// The turn kinds. Notice is the recorder's own voice (session banners,
// truncation, and budget aborts), never the agent's.
const (
	KindText       Kind = "text"
	KindThinking   Kind = "thinking"
	KindToolUse    Kind = "tool_use"
	KindToolResult Kind = "tool_result"
	KindNotice     Kind = "notice"
)

// Syntax is a harness-neutral rendering hint for a tool turn's Text. Empty
// means "infer from Path, or show plain".
type Syntax string

// The syntax hints.
const (
	SyntaxDiff  Syntax = "diff"
	SyntaxShell Syntax = "shell"
)

// Turn is one entry in an agent's conversation, normalised across harnesses.
// Text carries the message body, the tool invocation's rendered input, or the
// tool result's excerpt depending on Kind; Truncated reports that the recorder
// capped it.
//
// ID, Path and Syntax are optional and were added within Version 1: ID pairs a
// tool_use turn with its tool_result, Path names the file a tool call targets,
// and Syntax hints how to render Text. Records written before them decode with
// the fields empty.
type Turn struct {
	V         int    `json:"v"`
	Seq       int    `json:"seq"`
	At        string `json:"at,omitempty"`
	Role      Role   `json:"role"`
	Kind      Kind   `json:"kind"`
	Tool      string `json:"tool,omitempty"`
	ID        string `json:"id,omitempty"`
	Path      string `json:"path,omitempty"`
	Syntax    Syntax `json:"syntax,omitempty"`
	Text      string `json:"text,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// Encode renders a turn as its stdout line, without the trailing newline.
func Encode(t Turn) (string, error) {
	t.V = Version
	raw, err := json.Marshal(t)
	if err != nil {
		return "", fmt.Errorf("transcript: encode: %w", err)
	}
	return Prefix + string(raw), nil
}

// Decode recovers a turn from one log line; ok is false for any line that is
// not a turn line.
func Decode(line []byte) (Turn, bool) {
	rest, found := bytes.CutPrefix(bytes.TrimSpace(line), []byte(Prefix))
	if !found {
		// Kubernetes log lines may carry timestamps or the runtime may have
		// wrapped the line; find the prefix anywhere.
		if i := strings.Index(string(line), Prefix); i >= 0 {
			rest = line[i+len(Prefix):]
		} else {
			return Turn{}, false
		}
	}
	var t Turn
	if err := json.Unmarshal(rest, &t); err != nil {
		return Turn{}, false
	}
	if t.V != Version || t.Kind == "" {
		return Turn{}, false
	}
	return t, true
}

// HasPrefix reports whether a log line is a turn line. Callers scanning for
// some other prefix use it to skip turns cheaply, without a full decode.
func HasPrefix(line []byte) bool {
	return bytes.Contains(line, []byte(Prefix))
}

// Truncated reports whether any turn in a recorded transcript was cut, so a
// reader can be told the record is partial. It is derived from the turns
// themselves rather than reported alongside them, because only what survived
// into storage reliably shows what was kept.
func Truncated(turns []Turn) bool {
	for _, t := range turns {
		if t.Truncated {
			return true
		}
	}
	return false
}

// Truncate caps s at limit bytes on a rune boundary (the API server rejects
// invalid UTF-8, and a turn is stored in a ConfigMap). It reports whether it
// cut anything.
func Truncate(s string, limit int) (string, bool) {
	if len(s) <= limit {
		return s, false
	}
	cut := s[:limit]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut, true
}

// Compact bounds a recorded conversation for storage. Each turn is capped at
// MaxTurnBytes; when the turns exceed MaxTurns or their text exceeds
// MaxTotalBytes, the head and the tail are kept (how the run started and how
// it ended) and one notice in between says how many turns were elided. Zero
// fields take StoreDefaults; negative fields disable that bound. Turns keep
// their original Seq, so a stored record and the live stream agree on
// numbering; the notice reuses the Seq of the first turn it replaces.
//
// The input is not modified.
func Compact(turns []Turn, l Limits) []Turn {
	l = l.resolve(StoreDefaults)
	out := make([]Turn, len(turns))
	copy(out, turns)
	if l.MaxTurnBytes > 0 {
		for i := range out {
			if cut, did := Truncate(out[i].Text, l.MaxTurnBytes); did {
				out[i].Text, out[i].Truncated = cut, true
			}
		}
	}

	// keep is how many turns fit, reserving one slot for the notice once any
	// must go.
	keep := len(out)
	if l.MaxTurns > 0 && keep > l.MaxTurns {
		keep = max(l.MaxTurns-1, 0)
	}
	if l.MaxTotalBytes > 0 {
		// pre[i] is the text size of the first i turns, so each candidate
		// head/tail split is sized in constant time.
		pre := make([]int, len(out)+1)
		for i, t := range out {
			pre[i+1] = pre[i] + len(t.Text)
		}
		size := func(keep int) int {
			head, tail := split(keep)
			return pre[head] + pre[len(out)] - pre[len(out)-tail]
		}
		for keep > 0 && size(keep) > l.MaxTotalBytes {
			keep--
		}
	}
	if keep == len(out) {
		return out
	}

	head, tail := split(keep)
	elided := len(out) - keep
	notice := Turn{
		Seq:       out[head].Seq,
		At:        out[head].At,
		Role:      RoleSystem,
		Kind:      KindNotice,
		Text:      fmt.Sprintf("%d turns elided from stored record", elided),
		Truncated: true,
	}
	res := make([]Turn, 0, keep+1)
	res = append(res, out[:head]...)
	res = append(res, notice)
	res = append(res, out[len(out)-tail:]...)
	return res
}

// split divides the keep retained turns between head and tail, favouring the
// head by one.
func split(keep int) (head, tail int) {
	head = (keep + 1) / 2
	return head, keep - head
}
