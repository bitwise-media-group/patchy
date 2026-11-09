// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package report

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v4"
)

var fence = []byte("---")

// splitFrontmatter separates a leading ----fenced YAML block from the
// markdown body that follows it.
func splitFrontmatter(data []byte) (block []byte, body string, err error) {
	rest, found := bytes.CutPrefix(bytes.TrimLeft(data, "\r\n"), fence)
	if !found {
		return nil, "", errors.New("report: missing frontmatter opening fence")
	}
	block, body2, found := bytes.Cut(rest, append([]byte("\n"), fence...))
	if !found {
		return nil, "", errors.New("report: unterminated frontmatter")
	}
	return block, string(bytes.TrimLeft(body2, "\r\n")), nil
}

// StripFrontmatter returns the markdown body following a leading ---fenced
// YAML block, or s unchanged when there is no complete fence. Stored reports
// keep their frontmatter (it is the machine contract the remediation stage
// re-parses); presentation seams — the status page, PR bodies, issue
// comments — call this before rendering.
func StripFrontmatter(s string) string {
	_, body, err := splitFrontmatter([]byte(s))
	if err != nil {
		return s
	}
	return body
}

// decodeStrict unmarshals the frontmatter block into out, rejecting unknown
// keys — the prompt promised a schema; hold the model to it.
func decodeStrict(block []byte, out any) error {
	dec := yaml.NewDecoder(bytes.NewReader(block))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("report: frontmatter: %w", err)
	}
	return nil
}

// summaryLine captures an indented summary key and its value — the only
// free-text scalars in the investigation frontmatter.
var summaryLine = regexp.MustCompile(`^([ \t]+summary:[ \t]+)(.*)$`)

// repairSummaries double-quotes unquoted summary values. Models write the
// summaries as plain prose, and prose containing a colon ("CWE-614: cookie
// lacks Secure") is not a valid plain scalar. Quoting a plain scalar that
// was already valid preserves its string value, so every unquoted summary
// is quoted; callers only attempt this after a strict parse has failed.
func repairSummaries(block []byte) ([]byte, bool) {
	lines := strings.Split(string(block), "\n")
	changed := false
	for i, line := range lines {
		m := summaryLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		val := strings.TrimRight(m[2], " \t")
		if val == "" {
			continue
		}
		switch val[0] {
		case '"', '\'', '|', '>', '&', '*':
			// Already quoted, a block scalar, or anchored — leave it.
			continue
		}
		val = strings.ReplaceAll(val, `\`, `\\`)
		val = strings.ReplaceAll(val, `"`, `\"`)
		lines[i] = m[1] + `"` + val + `"`
		changed = true
	}
	if !changed {
		return block, false
	}
	return []byte(strings.Join(lines, "\n")), true
}
