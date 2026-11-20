// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package runarchive assembles one agent run's long-term record and decides
// what a run's status keeps when archival is configured.
//
// A controller sees an agent's full output exactly once, when it collects the
// finished Job's log. Archive turns that output into immutable objects under
// one prefix per run attempt:
//
//	<prefix>/<namespace>/<finding>/<run-name>/<run-uid>/
//	    transcript.jsonl.gz   every streamed turn, uncompacted
//	    report.md             the full raw report, frontmatter included
//	    patch.mbox            remediation: git format-patch --binary
//	    changeset.json.gz     remediation: the changeset patchy pushes
//	    manifest.json         written last: the run, its accounting, and the
//	                          sha256 and size of every object above
//	    push.json             remediation: branch, commit and PR, after the push
//
// The run UID is in the path because finding and run names are reused after
// the TTL. Every object's bytes are a pure function of the run's output (no
// wall clock), so a re-collected run rewrites identical bytes and the
// create-only store accepts the retry.
//
// Collector is the decision both run controllers share: archive and record
// references (no ConfigMap), retry within a window while the pod log is still
// readable, and past the window either fall back to the ConfigMap transcript
// (fail-open, the default) or fail the run (required).
package runarchive
