// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package build is the engine behind `patchy build`: it builds the
// first-party artifacts the patchy config's build: block lists — Dockerfile
// images and Flux manifest directories — scans them before anything is
// pushed, publishes them to every configured registry, and signs them with
// that registry's signing block. It is mirror's sibling for artifacts this
// project produces rather than vendors, and reuses mirror's seams wholesale:
// ocireg (pushes and existence probes), scan (archive scanners, the
// blocking policy, kubescape), allowlist (lint and regeneration), sign (the
// verify-then-sign Ensurer) and spec (registries, signing, the one scan
// block).
//
// The flows: an image entry expands its tags and args (failing fast on an
// unset variable or an invalid tag), lints its allowlist
// (security/allowlists/<name>.yaml), builds an OCI layout with docker
// buildx, scans each platform image as a docker archive, and fails with
// nothing pushed if a blocking finding survives the allowlist. A manifests
// entry stages its filtered tree, sweeps it with kubescape, and packages it
// as a Flux OCI artifact (fluxartifact). Publishing never moves a tag: an
// absent tag is pushed, one already at this digest is skipped, and one at
// another digest is left alone with a warning. Dry runs build and scan but
// never contact a registry; UpdateAllowlist rebuilds and rescans to
// regenerate an allowlist without publishing anything.
//
// The engine never discovers config files and never touches git: the CLI
// hands it a resolved config, and the calling pipeline owns commits.
package build
