// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package helmchart moves chart archives between upstreams, the vendored
// tree and the mirror registry. Pull fetches the pinned tgz from an oci://
// repository (the chart content layer, byte-identical to what helm pull
// writes) or an https:// helm repository (index.yaml, then the release
// asset); Extract unpacks it deterministically and traversal-safely into
// the vendor tree; TreeDiff compares an extraction against the committed
// tree, the publish-time tripwire that catches upstream mutating a
// released version in place.
//
// The tgz itself is never committed: the vendored tree is what reviews
// see, and the lock's upstreamTgzSha256 proves the published artifact is
// the reviewed one.
package helmchart
