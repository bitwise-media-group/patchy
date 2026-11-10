// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package fluxartifact packages a directory of Flux manifests as a Flux
// OCI artifact — the shape `flux push artifact` produces and an
// OCIRepository source consumes: an OCI image manifest whose config is
// application/vnd.cncf.flux.config.v1+json ({}) and whose single layer is
// the tree as application/vnd.cncf.flux.content.v1.tar+gzip, annotated with
// the source URL and revision.
//
// The artifact is content-addressed: the tarball is deterministic (sorted
// relative paths, normalized modes, zeroed times and owners, a gzip header
// with no name or mtime) and the manifest carries no created annotation,
// so the same tree always yields the same digest and a re-publish is a
// no-op.
//
// Stage copies the tree first, dropping gitignore-syntax ignore patterns
// (.git/ always) and refusing anything but regular files and directories
// — the staged copy is exactly what gets scanned and packaged.
package fluxartifact
