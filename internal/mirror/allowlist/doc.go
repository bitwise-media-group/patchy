// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package allowlist derives an entry's allowlist (a mirror entry's
// security/allowlist.yaml, a build entry's security/allowlists/<name>.yaml)
// from the blocking findings of a fresh scan, and lints any allowlist
// against the accepted-risk policy — mirror and build share both halves,
// so the two apply identical rules. Hand-curating a per-CVE list stops
// being review and starts being transcription once images bump on their own
// schedule — so the machine transcribes, and the PR review is where the
// risk is actually accepted.
//
// Three rules make the regenerated file trustworthy rather than a rubber
// stamp: findings that no longer appear are DROPPED (a stale accept is
// indistinguishable from a live one); surviving entries KEEP their original
// expired_at (regeneration must never roll the clock forward — the expiry
// is the whole forcing function; new entries get today+newDays); and
// per-entry notes are preserved verbatim (statements are machine facts,
// human reachability analysis lives in notes).
//
// Everything here is pure — findings in, entries and rendered bytes out —
// except Regenerate, which writes the rendered file. The wall clock is a
// parameter, and derivation runs only on the explicit update paths (mirror
// upgrade, build --update-allowlists) — never validate — because the
// finding set moves with the vulnerability databases.
package allowlist
