// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package cli builds the patchy command tree.
//
// The grammar is Kubernetes-standard: patchy <verb> <noun> [name...]. Verbs are
// cobra commands, nouns are resolved through the resource registry, and adding
// a noun never adds a verb. That is what keeps the surface predictable as the
// supply-chain nouns (chart, image, allowlist) arrive alongside the pipeline
// ones.
//
// Two rules hold everywhere in this package:
//
//   - stdout is data, stderr is narration. Anything a script might parse goes to
//     stdout and nothing else does, so `patchy get findings -o json | jq` works
//     with logging turned all the way up.
//   - presentation is decided once, from the output format and whether stdout is
//     a terminal — never per call site. See internal/printer.
package cli
