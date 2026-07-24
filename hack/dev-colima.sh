#!/bin/sh
# Copyright 2026 Bitwise Media Group Ltd.
# SPDX-License-Identifier: MIT
#
# One-command local dev loop on Colima: start the VM+k3s if needed, snapshot
# the images, retag the host-arch ones as patchy/<app>:dev (the tags the dev
# overlay expects — the shared Docker/k3s image store makes the tag the whole
# "load" step), apply the overlay, and restart the deployments so a re-run
# redeploys the fresh build. See docs/deployment/colima.md.
set -eu

for tool in colima docker kubectl docker-buildx; do
  command -v "$tool" >/dev/null || {
    echo "error: $tool not found (dotty brew add $tool || brew install $tool)" >&2
    exit 1
  }
done

# start colima with k8s if it isn't running; if it is, trust it and just
# verify the colima kube context answers (catches a colima started without
# --kubernetes). Passing any --k3s-arg replaces colima's default
# [--disable=traefik], so an inert arg (--tls-san=localhost) keeps the
# bundled traefik ingress controller enabled — the dev overlay ships an
# Ingress for the integration-controller that it satisfies.
colima status >/dev/null 2>&1 || colima start --kubernetes \
  --cpu "${COLIMA_CPU:-4}" --memory "${COLIMA_MEMORY:-8}" \
  --k3s-arg=--tls-san=localhost
kubectl --context colima get --raw /readyz >/dev/null || {
  echo "error: colima is running but k3s is not reachable — start with --kubernetes" >&2
  exit 1
}
kubectl --context colima get ingressclass traefik >/dev/null 2>&1 || {
  echo "note: no traefik IngressClass — this colima predates the traefik-enabled start;" >&2
  echo "      'colima stop' and re-run to reinstall k3s with the ingress controller" >&2
}

mise run snapshot

# retag the host-arch snapshot images as patchy/<app>:dev (dev overlay names)
arch=$(uname -m)
[ "$arch" = x86_64 ] && arch=amd64
for app in integration-controller source-controller context-controller investigation-controller remediation-controller claude-agent-runner codex-agent-runner status-server; do
  tag=$(docker images "ghcr.io/bitwise-media-group/patchy/$app" --format '{{.Tag}}' |
    grep -- "-$arch$" | head -1)
  [ -n "$tag" ] || {
    echo "error: no snapshot image for $app ($arch)" >&2
    exit 1
  }
  docker tag "ghcr.io/bitwise-media-group/patchy/$app:$tag" "patchy/$app:dev"
done

# PATCHY_OVERLAY=dev-fake swaps in the credential-less stack: the scripted
# agent image (hack/fake-agent) and the fake GitHub running in-cluster
# (e2e/fakegithub, cross-compiled here — the Dockerfile carries no Go
# toolchain, matching Dockerfile.controller). See docs/deployment/colima.md.
overlay="${PATCHY_OVERLAY:-dev}"
if [ "$overlay" = dev-fake ]; then
  docker build -t patchy/fake-agent:dev hack/fake-agent
  mkdir -p bin
  (cd e2e && CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
    go build -trimpath -o ../bin/fakegithub ./fakegithub/cmd)
  docker build -t patchy/fakegithub:dev -f hack/fakegithub/Dockerfile bin
fi

# CRDs first, and wait until they are Established: the overlay contains
# Integration/Forge CRs, and a single apply on a fresh cluster races the
# discovery cache ("resource mapping not found ... ensure CRDs are
# installed first").
kubectl --context colima apply -f deploy/kustomize/base/crds/
kubectl --context colima wait --for=condition=Established -f deploy/kustomize/base/crds/ --timeout=60s

kubectl --context colima apply -k "deploy/kustomize/overlays/$overlay"

# optional PAT: replace the dev overlay's placeholder patchy-github Secret
# (the Integration/Forge CRs reference it) with a real token so GitHub calls
# work. The webhookSecret stays the dev placeholder unless overridden.
if [ -n "${GITHUB_TOKEN:-}" ]; then
  printf '%s' "$GITHUB_TOKEN" |
    kubectl --context colima -n patchy create secret generic patchy-github \
      --from-file=token=/dev/stdin \
      --from-literal=webhookSecret="${PATCHY_WEBHOOK_SECRET:-dev-webhook-secret-replace-me}" \
      --dry-run=client -o yaml |
    kubectl --context colima apply -f -
else
  echo "note: GITHUB_TOKEN not set — GitHub calls will fail on the placeholder" >&2
  echo "      credential; see docs/deployment/colima.md#github-credentials" >&2
fi

# same :dev tag every run — restart so pods pick up the retagged image
kubectl --context colima -n patchy rollout restart deployment -l app.kubernetes.io/part-of=patchy
kubectl --context colima -n patchy rollout status deployment -l app.kubernetes.io/part-of=patchy --timeout=120s

# where the ingress answers depends on colima's network mode: with a reachable
# VM address colima deliberately does NOT forward guest 80/443 to the host
# (traefik is reached at the VM IP); without one, lima forwards them and
# localhost works. Probe for the webhook route: GET on a POST-only handler
# answering 405 proves the request reached the controller.
addr=$(colima ls --json 2>/dev/null | grep '"name":"default"' | sed -n 's/.*"address": *"\([^"]*\)".*/\1/p')
ingress="http://${addr:-localhost}/github/webhooks"
code=000
for _ in 1 2 3 4 5 6 7 8 9 10; do
  code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 2 "$ingress" || true)
  [ "$code" = "405" ] && break
  sleep 2
done
[ "$code" = "405" ] || echo "note: $ingress answered $code, not the expected 405 — traefik may still be starting" >&2
echo "patchy dev stack ready — webhook ingress: $ingress  nodeport: http://localhost:30079/healthz"
