// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package context is the context-controller's engine: it reacts to freshly
// opened Findings, runs the enhancer chain (CMDB ownership, infrastructure
// context — pkg/enhance plugins), records each contribution as an enrichment
// on the Finding's status, and advances Opened → Enhanced. It holds no
// tracking-system credential — projecting enrichments as issue comments is
// integration-controller work.
//
// Enhancement is best-effort by design: a failing enhancer is logged and
// skipped, and the transition happens regardless — enrichment must never
// block the pipeline.
package context
