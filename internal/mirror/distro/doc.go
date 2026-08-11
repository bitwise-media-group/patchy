// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package distro derives extra mirrored images from a distribution
// manifests artifact, for charts whose workload images are deployed by an
// operator from manifests helm never renders — so image discovery cannot
// see them, and hand-pinned extras drift the moment upstream cuts a
// release. The artifact (pulled at the tag matching the vendored chart's
// appVersion) carries <product>/<version>/<component>.yaml manifest trees;
// the newest version present is what the pinned operator deploys, and each
// named component's Deployment image joins the derived set.
//
// The derived entries land in the generated images.extra.yaml sidecar,
// never in manifest.yaml: the manifest stays a pure intent file, and the
// sidecar merges in during discovery.
package distro
