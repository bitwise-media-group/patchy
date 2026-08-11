// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package semverpick selects versions from candidate lists: the newest
// stable release satisfying a constraint (chart and artifact update checks),
// and the newest tag that has soaked past a cooldown window (tracked-image
// picks, where a just-published tag can still be yanked or hot-fixed).
//
// Only release versions of the shape X.Y.Z — optionally v-prefixed — are
// ever considered; pre-releases and build metadata are filtered out before
// constraint evaluation, because the mirror only ships releases. All
// wall-clock input is injected, keeping the package deterministic under
// test.
package semverpick
