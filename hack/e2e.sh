#!/usr/bin/env sh
# Copyright 2026 Bitwise Media Group Ltd.
# SPDX-License-Identifier: MIT
#
# Run the end-to-end suite: an envtest kube-apiserver carries the CRD state
# machine, the real controller binaries run against it, and a fake GitHub
# API stands in at the network boundary. setup-envtest downloads the
# kube-apiserver/etcd binaries once into ~/.cache; the suite skips itself
# when KUBEBUILDER_ASSETS is empty, so plain `go test` stays fast.
set -eu

KUBEBUILDER_ASSETS=$(setup-envtest use --bin-dir "${HOME}/.cache/kubebuilder-envtest" -p path)
export KUBEBUILDER_ASSETS
cd e2e
go test ./... -count=1 "$@"
