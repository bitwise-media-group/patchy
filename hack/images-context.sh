#!/bin/sh
# Copyright 2026 Bitwise Media Group Ltd.
# SPDX-License-Identifier: MIT
#
# Cross-compile the binaries patchy's images copy in, into the build context
# patchy.yaml's image entries use: dist/images/linux/<arch>/<binary>, the
# $TARGETPLATFORM/<binary> layout of goreleaser's dockers_v2 context, so the
# same Dockerfile.* serve both. Same ldflags contract as .goreleaser.yaml.
#
# In an edge build (the patchy-publish action's prepare step, CHANNEL=edge)
# the binaries report the version their image is tagged with, from the same
# VERSION, PATCHY_TIMESTAMP and GIT_COMMIT the tag template expands:
# <VERSION>-edge.<PATCHY_TIMESTAMP>.g<short commit>. Otherwise VERSION (or
# git describe) is used as-is. The status page SPA must be built first
# (mise run ui; the images-context task does it).
set -eu

out=dist/images
module=$(go list -m)
commit=${GIT_COMMIT:-$(git rev-parse HEAD)}
short=$(printf '%.7s' "$commit")
if [ "${CHANNEL:-}" = edge ]; then
  version="${VERSION:?VERSION is the next release}-edge.${PATCHY_TIMESTAMP:?}.g${short}"
else
  version=${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}
fi
epoch=${SOURCE_DATE_EPOCH:-$(git log -1 --format=%ct)}
date=$(date -u -d "@$epoch" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -r "$epoch" +%Y-%m-%dT%H:%M:%SZ)
ldflags="-s -w \
  -X $module/internal/version.Version=$version \
  -X $module/internal/version.Commit=$short \
  -X $module/internal/version.BuildDate=$date"

# One binary per image (the agent-runner serves both runner images).
binaries="source-controller context-controller investigation-controller remediation-controller
integration-controller evaluation-controller egress-broker status-server agent-runner"

rm -rf "$out"
for arch in amd64 arm64; do
  for app in $binaries; do
    tags=""
    [ "$app" = status-server ] && tags=withui
    CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -tags "$tags" -ldflags "$ldflags" \
      -o "$out/linux/$arch/$app" "./cmd/$app"
  done
done
echo "images-context: $version into $out/"
