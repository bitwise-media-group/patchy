// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package buildx drives `docker buildx build` for patchy build. It builds
// the argv for one image entry and runs it through the same tool runner the
// scanners use. The build exports an OCI layout directory instead of
// loading into a daemon or pushing, so nothing reaches a registry until the
// scan has passed.
//
// With SOURCE_DATE_EPOCH set the export rewrites layer timestamps, so a
// rebuild of the same commit yields the same digest and a re-run lands on
// skipped-current instead of a tag conflict.
package buildx
