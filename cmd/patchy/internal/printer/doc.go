// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package printer turns results into output.
//
// This package decides once whether output is for a person or a program. A
// terminal gets styled tables and rendered markdown. A pipe gets plain text,
// no escape codes, and aligned columns awk can split. Commands never decide
// this themselves, so no command can decide it differently.
//
// The styled and plain paths lay columns out identically (only styling
// differs), so a screenshot and a pasted pipe show the same shape. Two
// renderers exist because ANSI escapes break byte-counting alignment:
// text/tabwriter is correct when there are no escapes, lipgloss is correct
// when there are.
package printer
