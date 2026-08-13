#!/usr/bin/env sh
# Copyright 2026 Bitwise Media Group Ltd.
# SPDX-License-Identifier: MIT
#
# Run the Tier-1 microbenchmark suite, recording the numbers under
# coverage/bench/ (git-ignored) so successive runs can be compared with
# benchstat, then run the complexity-ratio assertions. The two passes are
# separate on purpose: a red assertion (expected on a pre-fix tree) must not
# suppress the benchmark numbers, which `go test` would do if a test failed
# in the same invocation. See docs/dev/benchmarks.md for the targets and the
# 2M extrapolation method.
set -eu

mkdir -p coverage/bench
stamp=$(date +%Y%m%d-%H%M%S)
out="coverage/bench/${stamp}.txt"

# -p 1: benchmark binaries must not run concurrently — parallel packages
# compete for cores and poison every number.
go test -p 1 ./internal/... -run '^$' -bench . -benchmem -timeout 30m "$@" | tee "${out}"

echo
echo "recorded: ${out}"
echo "compare runs with: benchstat coverage/bench/<old>.txt ${out}"
echo

status=0
PATCHY_BENCH_ASSERT=1 go test -p 1 ./internal/... -run 'TestComplexity' -count=1 \
	-timeout 30m | tee -a "${out}" || status=$?
if [ "${status}" -ne 0 ]; then
	echo
	echo "complexity assertions FAILED (see above); the benchmark numbers are still recorded"
fi
exit "${status}"
