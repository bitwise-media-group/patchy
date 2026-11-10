// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package expand

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Lookup resolves one variable; ok is false when it is unset.
type Lookup func(name string) (value string, ok bool)

// TimestampVar is the built-in run timestamp's name.
const TimestampVar = "PATCHY_TIMESTAMP"

// TimestampLayout is the built-in's format: UTC yyyymmddhhmmss.
const TimestampLayout = "20060102150405"

// Expand substitutes every reference in s.
func Expand(s string, lookup Lookup) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		if c != '$' {
			b.WriteByte(c)
			i++
			continue
		}
		if i+1 >= len(s) {
			return "", fmt.Errorf("trailing $ at offset %d: write $$ for a literal $", i)
		}
		switch s[i+1] {
		case '$':
			b.WriteByte('$')
			i += 2
		case '{':
			end, err := closingBrace(s, i+2)
			if err != nil {
				return "", err
			}
			v, err := reference(s[i+2:end], lookup)
			if err != nil {
				return "", err
			}
			b.WriteString(v)
			i = end + 1
		default:
			return "", fmt.Errorf("bare $ at offset %d: write ${NAME} for a variable or $$ for a literal $", i)
		}
	}
	return b.String(), nil
}

// closingBrace finds the } closing a reference whose body starts at from,
// skipping nested ${...} (a default may itself reference variables).
func closingBrace(s string, from int) (int, error) {
	depth := 0
	for i := from; i < len(s); i++ {
		switch {
		case s[i] == '$' && i+1 < len(s) && s[i+1] == '$':
			i++
		case s[i] == '$' && i+1 < len(s) && s[i+1] == '{':
			depth++
			i++
		case s[i] == '}':
			if depth == 0 {
				return i, nil
			}
			depth--
		}
	}
	return 0, fmt.Errorf("unterminated ${ at offset %d", from-2)
}

// reference resolves one ${...} body.
func reference(body string, lookup Lookup) (string, error) {
	n := nameLen(body)
	if n == 0 {
		return "", fmt.Errorf("${%s}: expected a variable name", body)
	}
	name, rest := body[:n], body[n:]
	value, ok := lookup(name)
	switch {
	case rest == "":
		if !ok {
			return "", fmt.Errorf("${%s} is not set", name)
		}
		return value, nil
	case strings.HasPrefix(rest, ":-"):
		if ok && value != "" {
			return value, nil
		}
		return Expand(rest[2:], lookup)
	case strings.HasPrefix(rest, ":"):
		if !ok {
			return "", fmt.Errorf("${%s} is not set", name)
		}
		return substring(name, value, rest[1:])
	default:
		return "", fmt.Errorf("${%s}: unsupported syntax %q (want ${NAME}, ${NAME:-default} "+
			"or ${NAME:offset:length})", body, rest)
	}
}

// nameLen measures the leading [A-Za-z_][A-Za-z0-9_]* run.
func nameLen(s string) int {
	for i := 0; i < len(s); i++ {
		c := s[i]
		letter := c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
		if !letter && (i == 0 || c < '0' || c > '9') {
			return i
		}
	}
	return len(s)
}

// substring applies an offset[:length] spec, clamped like the shell's.
func substring(name, value, spec string) (string, error) {
	offStr, lenStr, hasLen := strings.Cut(spec, ":")
	off, err := nonNegative(offStr)
	if err != nil {
		return "", fmt.Errorf("${%s:%s}: offset: %w", name, spec, err)
	}
	runes := []rune(value)
	if off > len(runes) {
		off = len(runes)
	}
	end := len(runes)
	if hasLen {
		n, err := nonNegative(lenStr)
		if err != nil {
			return "", fmt.Errorf("${%s:%s}: length: %w", name, spec, err)
		}
		if off+n < end {
			end = off + n
		}
	}
	return string(runes[off:end]), nil
}

// nonNegative parses a plain decimal count.
func nonNegative(s string) (int, error) {
	if s == "" || strings.TrimLeft(s, "0123456789") != "" {
		return 0, fmt.Errorf("%q is not a non-negative integer", s)
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// Timestamp resolves the run's PATCHY_TIMESTAMP once: an explicit
// PATCHY_TIMESTAMP wins (so several jobs can share one value), else
// SOURCE_DATE_EPOCH (so a rebuild of the same commit stamps the same tag),
// else now — always UTC yyyymmddhhmmss.
func Timestamp(lookup Lookup, now time.Time) (string, error) {
	if v, ok := lookup(TimestampVar); ok && v != "" {
		if _, err := time.Parse(TimestampLayout, v); err != nil {
			return "", fmt.Errorf("%s=%q is not UTC yyyymmddhhmmss", TimestampVar, v)
		}
		return v, nil
	}
	if v, ok := lookup("SOURCE_DATE_EPOCH"); ok && v != "" {
		secs, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return "", fmt.Errorf("SOURCE_DATE_EPOCH=%q is not a Unix timestamp", v)
		}
		return time.Unix(secs, 0).UTC().Format(TimestampLayout), nil
	}
	if now.IsZero() {
		return "", errors.New("no clock")
	}
	return now.UTC().Format(TimestampLayout), nil
}

// WithBuiltins layers the built-in PATCHY_TIMESTAMP over lookup.
func WithBuiltins(lookup Lookup, timestamp string) Lookup {
	return func(name string) (string, bool) {
		if name == TimestampVar {
			return timestamp, true
		}
		return lookup(name)
	}
}
