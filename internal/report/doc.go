// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package report defines the two agent-written report contracts — the
// classification report and the remediation report — as YAML frontmatter
// schemas with strict, validated parsing. The prompts promise these exact
// shapes; unknown keys, missing required fields, and out-of-range values are
// errors, because everything downstream (labels, routing, budgets) is
// derived from these files.
package report
