#!/usr/bin/env sh
# Copyright 2026 Bitwise Media Group Ltd.
# SPDX-License-Identifier: MIT
#
# Run the tests that need a real API server. setup-envtest downloads the
# kube-apiserver/etcd binaries once into ~/.cache (the default store under
# ~/Library/Application Support is not writable in sandboxed agent runs) and
# the tests skip themselves when KUBEBUILDER_ASSETS is empty, so plain
# `go test` stays fast and network-free.
set -eu

KUBEBUILDER_ASSETS=$(setup-envtest use --bin-dir "${HOME}/.cache/kubebuilder-envtest" -p path)
export KUBEBUILDER_ASSETS
go test ./api/... ./internal/action/... "$@"
