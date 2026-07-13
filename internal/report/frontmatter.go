// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package report

import (
	"bytes"
	"errors"
	"fmt"

	"go.yaml.in/yaml/v3"
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
