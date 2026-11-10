// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package buildx drives `docker buildx build` for patchy build: it builds
// the argv for one image entry and runs it through the same tool-runner
// seam the scanners use, exporting an OCI layout directory instead of
// loading into a daemon or pushing — nothing reaches a registry until the
// scan has passed.
//
// With SOURCE_DATE_EPOCH set the export rewrites layer timestamps, so a
// rebuild of the same commit yields the same digest and a re-run lands on
// skipped-current instead of a tag conflict.
package buildx
