#!/usr/bin/env sh
# Copyright 2026 Bitwise Media Group Ltd.
# SPDX-License-Identifier: MIT
#
# Seed the canned status-page data (dev only): apply the demo Findings and
# FindingRollups, then patch their statuses, because `kubectl apply` cannot write a
# status subresource, so phases/rollup buckets need a second pass. Patching
# the /status endpoint with the full object as a merge patch is safe: the
# subresource endpoint persists only the status field.
#
# Safe to re-run. Uses the current kubeconfig context; point it at your dev
# cluster (colima/kind) first. MANIFEST overrides the default data file.
set -eu

manifest="${MANIFEST:-deploy/kustomize/overlays/dev/findings-demo.yaml}"

kubectl apply -f "$manifest"

tmp=$(mktemp -d "${TMPDIR:-/tmp}/status-demo.XXXXXX")
trap 'rm -rf "$tmp"' EXIT

# Split the multi-doc manifest; docs without a status block need no patch.
awk -v dir="$tmp" '/^---$/{n++; next}{print > (dir "/doc" n ".yaml")}' "$manifest"
for doc in "$tmp"/doc*.yaml; do
  grep -q '^status:' "$doc" || continue
  kubectl patch -f "$doc" --subresource=status --type=merge --patch-file "$doc"
done

echo "status demo data seeded; port-forward the status page to inspect:"
echo "  kubectl -n patchy port-forward svc/patchy-status-server 8080:8080"
