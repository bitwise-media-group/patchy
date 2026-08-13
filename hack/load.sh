#!/usr/bin/env sh
# Copyright 2026 Bitwise Media Group Ltd.
# SPDX-License-Identifier: MIT
#
# Run the Tier-2 load suite: an envtest kube-apiserver, the real controller
# binaries, and tens of thousands of findings driven through them. Opt-in by
# construction — the tests skip without PATCHY_LOAD=1 — and sized by
# PATCHY_LOAD_N (default 50000; a 10000 smoke run finishes much faster).
# Numbers land under coverage/bench/ beside the Tier-1 records. See
# docs/dev/benchmarks.md for the targets.
set -eu

KUBEBUILDER_ASSETS=$(setup-envtest use --bin-dir "${HOME}/.cache/kubebuilder-envtest" -p path)
export KUBEBUILDER_ASSETS

mkdir -p coverage/bench
stamp=$(date +%Y%m%d-%H%M%S)
out="coverage/bench/load-${stamp}.txt"

cd e2e
PATCHY_LOAD=1 go test -run 'TestLoad' -v -count=1 -timeout 60m "$@" | tee "../${out}"

echo
echo "recorded: ${out}"
