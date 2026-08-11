// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package yamledit

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

// Get returns the scalar value at path.
func Get(src []byte, path string) (string, error) {
	node, err := locate(src, path)
	if err != nil {
		return "", err
	}
	return node.Value, nil
}

// Set replaces the scalar at path with newValue, preserving every byte of
// the document outside the token itself, including the token's quoting
// style. The scalar's current value must equal oldValue, or the edit
// aborts — the callers replace known pins, they never invent values.
func Set(src []byte, path, oldValue, newValue string) ([]byte, error) {
	node, err := locate(src, path)
	if err != nil {
		return nil, err
	}
	if node.Value != oldValue {
		return nil, fmt.Errorf("value at %s is %q, expected %q", path, node.Value, oldValue)
	}
	start, err := byteOffset(src, node.Line, node.Column)
	if err != nil {
		return nil, fmt.Errorf("locate %s: %w", path, err)
	}
	// An anchored scalar's position covers the "&name " prefix; keep it.
	start += anchorPrefix(src[start:])
	length, style, err := tokenExtent(src[start:], node)
	if err != nil {
		return nil, fmt.Errorf("token at %s: %w", path, err)
	}
	token, err := renderScalar(newValue, style)
	if err != nil {
		return nil, fmt.Errorf("render replacement for %s: %w", path, err)
	}
	out := make([]byte, 0, len(src)-length+len(token))
	out = append(out, src[:start]...)
	out = append(out, token...)
	out = append(out, src[start+length:]...)
	return out, nil
}

// locate parses src and resolves path to a scalar node.
func locate(src []byte, path string) (*yaml.Node, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(src, &root); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return nil, fmt.Errorf("empty document")
	}
	steps, err := parsePath(path)
	if err != nil {
		return nil, err
	}
	node := root.Content[0]
	for _, step := range steps {
		node = resolveAlias(node)
		switch {
		case step.key != "":
			if node.Kind != yaml.MappingNode {
				return nil, fmt.Errorf("%s: %q is not a mapping", path, step.key)
			}
			var found *yaml.Node
			for i := 0; i+1 < len(node.Content); i += 2 {
				if node.Content[i].Value == step.key {
					found = node.Content[i+1]
					break
				}
			}
			if found == nil {
				return nil, fmt.Errorf("%s: key %q not found", path, step.key)
			}
			node = found
		default:
			if node.Kind != yaml.SequenceNode {
				return nil, fmt.Errorf("%s: index [%d] into a non-sequence", path, step.index)
			}
			if step.index < 0 || step.index >= len(node.Content) {
				return nil, fmt.Errorf("%s: index [%d] out of range (%d items)", path, step.index, len(node.Content))
			}
			node = node.Content[step.index]
		}
	}
	node = resolveAlias(node)
	if node.Kind != yaml.ScalarNode {
		return nil, fmt.Errorf("%s does not resolve to a scalar", path)
	}
	return node, nil
}

// resolveAlias follows an alias to its anchor target.
func resolveAlias(n *yaml.Node) *yaml.Node {
	if n.Kind == yaml.AliasNode && n.Alias != nil {
		return n.Alias
	}
	return n
}

// step is one path component: a mapping key or a sequence index.
type step struct {
	key   string
	index int
}

// pathToken matches the next component: .key, ."quoted key", or [N].
var pathToken = regexp.MustCompile(`^(?:\.([A-Za-z0-9_-]+)|\."((?:[^"\\]|\\.)*)"|\[([0-9]+)\])`)

// parsePath parses the yq-style subset: .a.b, .a."k.ey", .a[0].
func parsePath(path string) ([]step, error) {
	if !strings.HasPrefix(path, ".") && !strings.HasPrefix(path, "[") {
		return nil, fmt.Errorf("path %q must start with '.'", path)
	}
	var steps []step
	rest := path
	for rest != "" {
		m := pathToken.FindStringSubmatch(rest)
		if m == nil {
			return nil, fmt.Errorf("path %q: cannot parse %q", path, rest)
		}
		switch {
		case m[1] != "":
			steps = append(steps, step{key: m[1]})
		case m[2] != "":
			steps = append(steps, step{key: strings.ReplaceAll(m[2], `\"`, `"`)})
		default:
			idx, err := strconv.Atoi(m[3])
			if err != nil {
				return nil, fmt.Errorf("path %q: bad index %q", path, m[3])
			}
			steps = append(steps, step{index: idx})
		}
		rest = rest[len(m[0]):]
	}
	if len(steps) == 0 {
		return nil, fmt.Errorf("path %q selects the document root, not a scalar", path)
	}
	return steps, nil
}

// byteOffset converts a 1-based line/column (columns count characters, as
// the parser reports them) to a byte offset.
func byteOffset(src []byte, line, column int) (int, error) {
	offset := 0
	for l := 1; l < line; l++ {
		i := bytes.IndexByte(src[offset:], '\n')
		if i < 0 {
			return 0, fmt.Errorf("line %d beyond end of file", line)
		}
		offset += i + 1
	}
	for c := 1; c < column; c++ {
		if offset >= len(src) || src[offset] == '\n' {
			return 0, fmt.Errorf("column %d beyond end of line %d", column, line)
		}
		_, size := utf8.DecodeRune(src[offset:])
		offset += size
	}
	return offset, nil
}

// tokenExtent measures the raw token starting at buf[0] and reports its
// quoting style. Only single-line tokens are supported.
func tokenExtent(buf []byte, node *yaml.Node) (length int, style yaml.Style, err error) {
	if len(buf) == 0 {
		return 0, 0, fmt.Errorf("empty token")
	}
	switch buf[0] {
	case '"':
		for i := 1; i < len(buf); i++ {
			switch buf[i] {
			case '\\':
				i++
			case '"':
				return i + 1, yaml.DoubleQuotedStyle, nil
			case '\n':
				return 0, 0, fmt.Errorf("multi-line double-quoted scalars are not supported")
			}
		}
		return 0, 0, fmt.Errorf("unterminated double-quoted scalar")
	case '\'':
		for i := 1; i < len(buf); i++ {
			if buf[i] != '\'' {
				if buf[i] == '\n' {
					return 0, 0, fmt.Errorf("multi-line single-quoted scalars are not supported")
				}
				continue
			}
			if i+1 < len(buf) && buf[i+1] == '\'' {
				i++ // escaped quote
				continue
			}
			return i + 1, yaml.SingleQuotedStyle, nil
		}
		return 0, 0, fmt.Errorf("unterminated single-quoted scalar")
	case '|', '>':
		return 0, 0, fmt.Errorf("block scalars are not supported")
	default:
		// Plain scalar: for the single-line case the raw bytes are the
		// value itself; verify rather than assume.
		if node.Style != 0 {
			return 0, 0, fmt.Errorf("unsupported scalar style %v", node.Style)
		}
		v := []byte(node.Value)
		if len(buf) < len(v) || string(buf[:len(v)]) != node.Value {
			return 0, 0, fmt.Errorf("raw token does not match parsed value %q", node.Value)
		}
		if bytes.IndexByte(v, '\n') >= 0 {
			return 0, 0, fmt.Errorf("multi-line plain scalars are not supported")
		}
		return len(v), 0, nil
	}
}

// anchorPrefix measures a leading "&name " (anchor plus whitespace) so the
// splice preserves it and replaces only the value token.
func anchorPrefix(buf []byte) int {
	if len(buf) == 0 || buf[0] != '&' {
		return 0
	}
	i := 1
	for i < len(buf) && buf[i] != ' ' && buf[i] != '\t' && buf[i] != '\n' {
		i++
	}
	for i < len(buf) && (buf[i] == ' ' || buf[i] == '\t') {
		i++
	}
	return i
}

// plainSafe admits replacement values that can stand unquoted: the pins the
// mirror splices (versions, image references) all pass; anything else gets
// double-quoted for safety.
var plainSafe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@+-]*$`)

// renderScalar renders value in the original token's style.
func renderScalar(value string, style yaml.Style) (string, error) {
	switch style {
	case yaml.DoubleQuotedStyle:
		return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + `"`, nil
	case yaml.SingleQuotedStyle:
		return `'` + strings.ReplaceAll(value, `'`, `''`) + `'`, nil
	case 0:
		if plainSafe.MatchString(value) {
			return value, nil
		}
		return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + `"`, nil
	default:
		return "", fmt.Errorf("unsupported scalar style %v", style)
	}
}
