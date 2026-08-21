#!/bin/sh
# Copyright 2026 Bitwise Media Group Ltd.
# SPDX-License-Identifier: MIT
#
# Report built binary sizes: a summary table of every cmd/ binary as built
# into bin/ by hack/build.sh (release contract: -s -w -trimpath, CGO off),
# then the top N largest symbols and top N largest packages per binary,
# aggregated from `go tool nm -size`. -s strips the very symbol table nm
# reads, so the analysis links each binary once more, unstripped, into a
# temp dir — with the build cache warm from the build task these are
# link-only. Symbol sizes attribute text/data only — pclntab and type
# metadata scale with package count but stay unattributed — so the
# per-package numbers are for ranking, not accounting.
#
# SIZE_TOP overrides the list depth (default 10). BUILD_TAGS matches
# hack/build.sh (default withui).
set -eu

top="${SIZE_TOP:-10}"
tags="${BUILD_TAGS-withui}"
tmp="$(mktemp -d "${TMPDIR:-/tmp}/patchy-size.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT

apps=""
for dir in cmd/*/; do
  app="$(basename "$dir")"
  if [ ! -f "bin/$app" ]; then
    echo "hack/size.sh: bin/$app missing — run 'make build' first" >&2
    exit 1
  fi
  apps="$apps $app"
done

for app in $apps; do
  CGO_ENABLED=0 go build -trimpath -tags "$tags" \
    -o "$tmp/$app.sym" "./cmd/$app"
done

echo "=== binary sizes (bin/, release flags: -s -w -trimpath) ==="
for app in $apps; do
  printf '%s %s\n' "$(wc -c < "bin/$app" | tr -d '[:space:]')" "$app"
done | sort -rn | awk '{
  total += $1
  printf "  %8.1f MiB  %11d B  %s\n", $1 / 1048576, $1, $2
}
END { printf "  %8.1f MiB  %11d B  (total)\n", total / 1048576, total }'

# Both rankings count text/rodata/data symbols only: bss (B) is zeroed at
# runtime and occupies no file bytes (drbg.memory alone is a 32 MiB bss
# region), undefined (U) dynamic imports have no size at all, and aliased
# names at one address (typerel.* / _type:*) must count once.
for app in $apps; do
  go tool nm -size "$tmp/$app.sym" | awk '
    NF >= 4 && $2 + 0 > 0 && $3 ~ /^[TtRrDd]$/ && !seen[$1]++ {
      printf "%d %s %s\n", $2, $3, $4
    }' > "$tmp/$app.nm"

  echo ""
  echo "=== $app: top $top symbols ==="
  sort -rn "$tmp/$app.nm" | head -n "$top" \
    | awk '{ printf "  %11d B  %s  %s\n", $1, $2, $3 }'

  echo ""
  echo "=== $app: top $top packages (by symbol size) ==="
  # Package = symbol name up to the first dot after the last slash
  # (github.com/x/y.(*T).M -> github.com/x/y; runtime.mallocgc -> runtime).
  # go:itab/type: metadata symbols bucket under their prefix.
  awk '{
    n = split($3, a, "/")
    dot = index(a[n], ".")
    if (dot == 0) next
    a[n] = substr(a[n], 1, dot - 1)
    pkg = a[1]
    for (i = 2; i <= n; i++) pkg = pkg "/" a[i]
    size[pkg] += $1
  }
  END { for (p in size) printf "%d %s\n", size[p], p }
  ' "$tmp/$app.nm" | sort -rn | head -n "$top" \
    | awk '{ printf "  %11d B  %s\n", $1, $2 }'
done
