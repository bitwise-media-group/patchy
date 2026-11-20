// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package enhance is patchy's public plugin interface for context
// enhancement. An Enhancer adds first- or third-party context (CMDB
// ownership, associated infrastructure, runbooks) to a newly opened finding.
// It returns structured attributes the pipeline consumes (most importantly the
// owners used for assignment) and optional markdown. context-controller writes
// the result to the Finding's status, and integration-controller projects the
// markdown onto the tracking issue as one sticky comment per enhancer.
//
// Implementations may live outside this repository, so the exported API is
// self-contained and depends on nothing under internal/.
// Enhancers run as an ordered chain and must be best-effort: a failing
// enhancer is logged and skipped, never blocking the pipeline.
package enhance
