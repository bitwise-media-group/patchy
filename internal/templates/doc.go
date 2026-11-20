// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package templates renders the markdown patchy writes (issue bodies,
// comments, pull-request bodies, finding descriptions, the agent handoff file
// and the stage prompts) from embedded text/templates, so the output stays
// consistent across the estate.
//
// The issue body is a one-way projection of the Finding CR. The integration
// controller re-renders it from the Finding whenever the rendered hash
// changes and never parses it back. It carries only a finding-name marker
// comment. The same rendering is the handoff file an agent investigates.
package templates
