#!/bin/sh
# Copyright 2026 Bitwise Media Group Ltd.
# SPDX-License-Identifier: MIT
#
# Build every patchy binary into bin/ — one controller per concern plus the
# agent runtime. Same ldflags contract as .goreleaser.yaml: version metadata
# stamped into internal/version. MODULE/VERSION/COMMIT/DATE/LDFLAGS override
# the derived defaults. BUILD_TAGS defaults to withui, which embeds the
# status page SPA (internal/web/ui/dist — build it first: mise run ui);
# BUILD_TAGS='' compiles the stub instead.
set -eu

module="${MODULE:-$(go list -m)}"
version="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
commit="${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo none)}"
date="${DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"
ldflags="${LDFLAGS:--s -w \
  -X $module/internal/version.Version=$version \
  -X $module/internal/version.Commit=$commit \
  -X $module/internal/version.BuildDate=$date}"
tags="${BUILD_TAGS-withui}"
mkdir -p bin
for dir in cmd/*/; do
  app=$(basename "$dir")
  CGO_ENABLED=0 go build -trimpath -tags "$tags" -ldflags "$ldflags" -o "bin/$app" "./cmd/$app"
done
