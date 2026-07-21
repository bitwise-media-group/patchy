// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package source is the source-controller's engine: the
// reconcilers for the Forge and Repository kinds. The Forge reconciler
// validates credentials and stamps readiness; the Repository reconciler
// resolves the covering Forge, pins the head SHA exactly once, downloads the
// forge's tarball archive at that SHA (pure HTTP — controllers carry no git
// binary), and publishes it through the artifact store for agent jobs to
// fetch credential-lessly.
package source
