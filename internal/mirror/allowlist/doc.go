// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package allowlist derives an entry's allowlist (a mirror entry's
// security/allowlist.yaml, a build entry's security/allowlists/<name>.yaml)
// from the blocking findings of a fresh scan, and lints any allowlist
// against the accepted-risk policy. Mirror and build share both halves, so
// the two apply identical rules. Once images bump on their own schedule,
// hand-curating a per-CVE list is transcription, not review. The machine
// transcribes, and the PR review is where the risk is accepted.
//
// Three rules keep the regenerated file from being a rubber stamp:
//   - findings that no longer appear are dropped (a stale accept looks the
//     same as a live one);
//   - surviving entries keep their original expired_at (regeneration must
//     never roll the clock forward, since the expiry is what forces a
//     review; new entries get today+newDays);
//   - per-entry notes are preserved verbatim (statements are machine facts,
//     human reachability analysis lives in notes).
//
// Everything here is pure (findings in, entries and rendered bytes out)
// except Regenerate, which writes the rendered file. The wall clock is a
// parameter. Derivation runs only on the explicit update paths (mirror
// upgrade, build --update-allowlists), never in validate, because the
// finding set moves with the vulnerability databases.
package allowlist
