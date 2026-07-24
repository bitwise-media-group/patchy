// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package printer turns results into output.
//
// One decision is made once, here, and everything else follows from it: is this
// output for a person or for a program? A terminal gets styled tables and
// rendered markdown; a pipe gets plain text, no escape codes, aligned columns
// awk can split. Commands never ask that question themselves, so no command can
// answer it differently.
//
// The styled and plain paths lay columns out identically — only styling
// differs — so a screenshot and a pasted pipe show the same shape. Two renderers
// exist because ANSI escapes break byte-counting alignment: text/tabwriter is
// correct when there are no escapes, lipgloss is correct when there are.
package printer
