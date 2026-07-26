// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package source is patchy's public plugin seam for security-finding
// sources. A source turns tool-specific deliveries — GitHub Advanced Security
// code-scanning alerts, Google Cloud Security Command Center notifications,
// other SAST tools, agentic findings — into normalized Findings. The
// integration-controller owns everything downstream (accumulation, issue
// projection, labels), so a new source implements Handler and nothing more.
//
// A source may also implement Resolver, the optional write-back: telling the
// originating tool what patchy decided, so a finding dismissed here does not
// stay open there.
//
// Findings are not always about code. A cloud finding names a CloudResource
// rather than a Repo, and its repository — if it has one — is resolved later
// by a pkg/enhance enhancer from the resource's ownership labels.
//
// Implementations may live outside this repository; the exported API is
// deliberately self-contained and depends on nothing under internal/.
// Dependencies a handler needs to enrich a delivery (an API client to fetch
// the full alert, say) are injected at construction, not passed per call.
package source
