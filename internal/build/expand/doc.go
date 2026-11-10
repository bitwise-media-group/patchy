// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package expand substitutes environment references into build tag, arg
// and annotation templates. The syntax is a deliberately small, strict
// subset of the shell's:
//
//	${VAR}                 the value of VAR; unset is an error
//	${VAR:-default}        VAR, or default when VAR is unset or empty
//	${VAR:offset:length}   a substring of VAR (${GITHUB_SHA:0:7})
//	$$                     a literal $
//
// Any other $ is an error rather than a literal, so a forgotten brace
// fails the build instead of publishing a tag with a $ in it. PATCHY_TIMESTAMP
// is a built-in: a UTC yyyymmddhhmmss fixed once per run (Timestamp).
//
// Everything here is pure; the caller supplies the lookup.
package expand
