#!/usr/bin/env bash
# Copyright 2026 Bitwise Media Group Ltd.
# SPDX-License-Identifier: MIT

# govulncheck with a reviewed acceptance list. govulncheck has no ignore
# mechanism of its own, so this wrapper fails on any advisory whose
# vulnerable symbols are actually reachable from our code EXCEPT the ones
# accepted below — each of which needs a reason and should be re-reviewed
# whenever the dependency that drags it in moves.
set -euo pipefail

# GO-2026-5932: golang.org/x/crypto/openpgp is unmaintained and flagged
# wholesale, with no fixed version existing or planned. It reaches the
# module through cosign's CLI/library packages (PGP key support inside
# cosign, not exercised by patchy — the mirror signs keylessly or via KMS
# and verifies cosign signatures only). Every cosign v3 consumer carries
# this advisory; drop it from this list when cosign sheds the dependency.
# GO-2026-6225: docker-credential-acr-env leaks the Azure AD token to any
# host merely containing an azurecr suffix (unanchored regex), with no fixed
# version — the repo is unmaintained. It reaches the module through cosign's
# cli/options keychain wiring, but cosign only puts the helper into the
# active keychain when RegistryOptions.KubernetesKeychain is set, and
# internal/mirror/sign leaves that zero-valued, so the vulnerable path is
# never invoked. Drop when cosign sheds or replaces the dependency.
accepted="GO-2026-5932 GO-2026-6225"

out="$(mktemp "${TMPDIR:-/tmp}/govulncheck.XXXXXX")"
trap 'rm -f "$out"' EXIT

govulncheck -format json ./... > "$out"

# Symbol-level findings only (trace[0].function set): package- and
# module-level matches are what `govulncheck ./...` reports as informative,
# not failures.
called="$(jq -r 'select(.finding and .finding.trace[0].function) | .finding.osv' "$out" | sort -u)"

fail=0
for osv in $called; do
  case " $accepted " in
    *" $osv "*) ;;
    *)
      echo "govulncheck: $osv reaches called symbols (see https://pkg.go.dev/vuln/$osv)" >&2
      fail=1
      ;;
  esac
done

if [ "$fail" -ne 0 ]; then
  echo "govulncheck: run 'govulncheck ./...' for traces; fix or accept in hack/govulncheck.sh with a reason" >&2
  exit 1
fi

for osv in $accepted; do
  if echo "$called" | grep -qxF "$osv"; then
    echo "govulncheck: $osv reachable — accepted (see hack/govulncheck.sh)" >&2
  fi
done
echo "govulncheck: clean (accepted: $accepted)" >&2
