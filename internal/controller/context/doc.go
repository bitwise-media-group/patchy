// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package context is the context-controller's engine. It reacts to newly
// opened Findings, runs the enhancer chain (CMDB ownership and infrastructure
// context, as pkg/enhance plugins), records each contribution as an
// enrichment on the Finding's status, and advances Opened → Enhanced. It holds
// no tracking-system credential: integration-controller projects enrichments
// as issue comments.
//
// Enhancement is best-effort. A failing enhancer is logged and skipped, and
// the transition happens anyway, because enrichment must never block the
// pipeline.
package context
