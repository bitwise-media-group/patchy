<!--
Copyright 2026 Bitwise Media Group Ltd.
SPDX-License-Identifier: MIT
-->

# Deploying patchy

Operator documentation for the Kubernetes deployment: what runs where, the GitHub App you must register, the Secrets and
custom resources you must create, and an honest account of what the agent sandbox does and does not guarantee.

For _what the system does_, read [DESIGN.md](../DESIGN.md); for _where the code lives_, read [AGENTS.md](../AGENTS.md).

Prefer Helm? [`helm/chart`](../helm/chart/README.md) renders this same stack (published to
`oci://ghcr.io/bitwise-media-group/patchy/charts/patchy` on release); everything below about the App, the Secrets, and
the sandbox applies to both.

## Layout

```text
deploy/
├── kustomize/
│   ├── base/                        # CRDs, namespaces, RBAC, config, deployments, services, netpol
│   ├── components/cilium/           # optional: FQDN egress policy for the agent sandbox (Cilium CNI)
│   ├── components/istio/            # optional: the same allowlist as a Sidecar + ServiceEntry (Istio mesh)
│   └── overlays/{dev,prod}/
└── README.md
```

The Dockerfiles live at the repo root: `Dockerfile.controller` (all five controllers, ARG `TARGET`) and
`Dockerfile.agent-runner` (the agent image: agent-runner + git + claude CLI).

## What gets deployed where

Two namespaces, and the split between them is the security boundary.

| Namespace       | Workload                                                                                                                                                      | Credentials it holds                                             |
| --------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------- |
| `patchy`        | `integration-controller` (the only internet-facing workload), `source-controller`, `context-controller`, `investigation-controller`, `remediation-controller` | reads the GitHub Secret referenced by your Integration/Forge CRs |
| `patchy-agents` | ephemeral agent `Job`s, created at runtime by the two job controllers                                                                                         | the model API key — and nothing else                             |

The custom resources in `patchy` — `Finding`, `Repository`, `Investigation`, `Remediation`, `FindingRollup`, plus the
`Integration`/`Forge` configuration kinds — **are** the state machine; etcd is the only state store. The CRDs render
first in the base (`base/crds/`, controller-gen output).

Each controller is a **single replica** with `strategy: Recreate`. The binaries do run leader election (a coordination
Lease per controller) as insurance against a botched rollout racing two replicas, but singleton-by-construction stays
the deployment model — do not scale these up.

Every controller talks to the Kubernetes API now, so every controller identity mounts its token and gets a verb-by-verb
`Role` in `patchy` (see `base/rbac.yaml`); the two job controllers additionally share a Role in `patchy-agents` (jobs,
pods, pods/log, secrets). The agent Job pods have **no RBAC at all** and run with `automountServiceAccountToken: false`
— a prompt-injected agent must not be able to read the very Secrets the isolation model depends on.

## Images

All six images are built and published by GoReleaser (`dockers_v2` in `.goreleaser.yaml`) as part of every release:
multi-arch (`linux/amd64` + `linux/arm64`) manifests pushed to `ghcr.io/bitwise-media-group/patchy/<name>` and tagged
`vX.Y.Z` + `latest`. GoReleaser compiles the binaries once and hands them to `docker buildx`; the repo-root
`Dockerfile.*` only assemble the runtime layer (`COPY $TARGETPLATFORM/<binary>`), so they cannot be `docker build`
directly from the repo. To build images locally, run `make snapshot` (needs docker + buildx) — it produces per-arch
`ghcr.io/...:v<next>-snapshot-<sha>-<arch>` images without pushing, ready for `kind load docker-image`. Each release
also uploads `digests.txt` and attests every image digest in it; verify with
`gh attestation verify --owner bitwise-media-group oci://ghcr.io/bitwise-media-group/patchy/<name>:vX.Y.Z`.

One Dockerfile builds all five controllers on the same `distroless/static` base; the per-image `build_args` in
`.goreleaser.yaml` set `TARGET` to pick the binary. Every controller is pure Go with no subprocesses — source-controller
downloads repository archives over the GitHub API and remediation-controller pushes the agent's changeset through the
Git Data API (`internal/ghpush`), so no controller image carries a `git` binary. Everything runs as uid 65532 with a
read-only root filesystem; `/tmp` is an `emptyDir` in every pod, which is what keeps the Go runtime's temp-file users
working.

The agent image is `debian:trixie-slim` carrying the `claude` CLI as Anthropic's self-contained native binary
(downloaded at build time from the official release bucket, sha256-verified against its manifest, pinned by
`ARG CLAUDE_VERSION` — Dependabot/renovate should bump it; versions are in lockstep with the `@anthropic-ai/claude-code`
npm package), plus `git`, `curl`, and `/bin/sh` for the init container's artifact fetch, and the `agent-runner` binary
on `PATH` under exactly that name (`internal/jobs` runs `Command: ["agent-runner"]`).

## GitHub App

Register one App for the whole pipeline and install it on the repositories patchy watches.

**Repository permissions:**

| Permission           | Access       | Why                                                          |
| -------------------- | ------------ | ------------------------------------------------------------ |
| Code scanning alerts | Read & write | read alert detail; dismiss false positives (DESIGN.md req 6) |
| Issues               | Read & write | the tracking projection — open, label, comment, close        |
| Contents             | Read & write | download the repository archive; push the remediation branch |
| Pull requests        | Read & write | open the PR the human reviews                                |
| Metadata             | Read         | mandatory                                                    |

**Webhook events to subscribe:** `code_scanning_alert`, `issues`, `issue_comment`, `pull_request`.

**Webhook URL — exactly one, pointed at the integration-controller:** `https://<your-host>/github/webhooks`. The
integration-controller is the single receiver: it validates each delivery against the webhook secrets of your configured
Integrations, ingests scanner events into Findings, and applies the human signals (issue close, `/approve`, PR merge).
No other controller serves a webhook. The base ships a ClusterIP Service and no Ingress: put your Ingress or Gateway in
front of `patchy-integration-controller:8080` in your own overlay.

Pipeline progress is **not** webhook-driven — the gates ("accumulation closed", "older than an hour", "a free
remediation slot") are conditions no event can announce. The controllers' watch-driven reconcile loops carry the
pipeline; the webhook path is ingestion and human-in-the-loop signals.

## Secrets and custom resources

Two Secrets, neither in git. Use SOPS or external-secrets; `base/secrets.example.yaml` is a commented template, not a
resource.

```sh
kubectl -n patchy create secret generic patchy-github \
  --from-literal=appID=123456 \
  --from-file=privateKey=./patchy.private-key.pem \
  --from-literal=webhookSecret="$(openssl rand -hex 32)"

# NOTE the namespace: the model key belongs to the AGENTS, not the controllers.
kubectl -n patchy-agents create secret generic patchy-anthropic \
  --from-literal=api-key="$ANTHROPIC_API_KEY"
```

`patchy-anthropic` must exist even for a fake-harness run: `internal/jobs` unconditionally wires `ANTHROPIC_API_KEY`
into the agent container from it, and a missing Secret means `CreateContainerConfigError` on every Job.

The pipeline is then switched on with two custom resources referencing that Secret — an `Integration` (webhook
validation, alert ingestion, issue projection) and a `Forge` (repository read for the artifact, write for the push +
PR). `base/crs.example.yaml` is the commented template; the dev overlay applies working placeholders
(`overlays/dev/crs-dev.yaml`). No Deployment mounts a GitHub credential: the controllers read the Secret through the
API, on demand, by name.

## Configuration

Everything is `PATCHY_*` environment in one ConfigMap (`base/configmap.yaml`), consumed with `envFrom`.
`internal/cli/options.go` maps each variable back onto a cobra flag — prefix `PATCHY`, dashes become underscores, so
`--agent-image` is `PATCHY_AGENT_IMAGE` — with precedence flag > env > default. The Deployments pass no flags but
`serve`, so the ConfigMap is the whole configuration surface. A key a binary does not bind is inert, which is why one
ConfigMap serves all five.

`PATCHY_AGENT_IMAGE` is a special case: it is the string the job controllers stamp into the Jobs they create, and
kustomize's `images:` transformer **does not rewrite ConfigMap values**. An overlay that pins the agent-runner image
must patch both the `images:` entry and this key.

## The isolation model — what it actually is

DESIGN.md requires the coding agent to run with "no internet access / no access to github APIs". Taken literally that is
unachievable: `claude -p` **is** a network client of `api.anthropic.com`. What is actually delivered:

**1. Credential absence — the real control.** The agent pod holds **no forge credential at all**, in any container. The
repository arrives as a tarball from source-controller's in-cluster artifact server: the URL carries an unguessable
128-bit id, the Job pins the sha256 digest, and the init container verifies it before extracting and synthesizing the
local git base. The per-Job Secret carries only handoff markdown; `internal/jobs` lists `GITHUB_TOKEN` in `reservedEnv`
so no configuration can smuggle a credential in. All GitHub side effects — issue projection, alert dismissal, branch
push, PRs — are performed controller-side with short-lived, per-repository scoped tokens. An agent that reaches
`github.com` reaches it as an anonymous member of the public.

**2. NetworkPolicy — the floor.** `patchy-agents` is default-deny in both directions. Egress is re-permitted for DNS,
the artifact port (9790) to source-controller only, and TCP 443 with the cluster's own ranges and the cloud metadata
endpoint (169.254.169.254) excluded. **A plain NetworkPolicy is L3/L4 and cannot match a hostname**, so "TCP 443" means
every HTTPS host on the internet, not just Anthropic's. Adjust the `except:` CIDRs in `base/networkpolicy.yaml` to your
cluster's pod/service/node CIDRs.

**3. CiliumNetworkPolicy — defence in depth, where the CNI supports it.** `components/cilium/networkpolicy-cilium.yaml`
(enabled by the prod overlay) narrows that egress to `toFQDNs`: `api.anthropic.com` and nothing else external — no
GitHub hosts appear in the agent's allowlist at all, because the pod never talks to a forge. Do not mistake the FQDN
policy for the boundary; the missing credential is the boundary.

On an Istio mesh, `components/istio` delivers the same allowlist instead: a `Sidecar` with `REGISTRY_ONLY` (exposing
only the `api.anthropic.com` ServiceEntry and the `patchy` namespace's artifact Service), matched by SNI. It requires
native sidecars (Kubernetes ≥ 1.29, istiod with `ENABLE_NATIVE_SIDECARS=true` — a classic sidecar hangs the Job) and the
Istio CNI node agent (`patchy-agents` enforces the `restricted` Pod Security Standard, which rejects `istio-init`). Two
differences from Cilium to keep in mind: the proxy does not constrain what names the pod may resolve, so DNS
exfiltration stays open; and enforcement lives inside the pod rather than on the node.

If you have neither Cilium nor Istio, drop the components. The base policy still applies and is then the whole of the
L3/L4 story.

## Applying

```sh
# Render and review first — both overlays must render clean.
kubectl kustomize deploy/kustomize/overlays/dev
kubectl kustomize deploy/kustomize/overlays/prod

kubectl apply -k deploy/kustomize/overlays/dev
```

The runbook order for a fresh cluster: apply the overlay (CRDs render first), create the two Secrets, apply your
`Integration`/`Forge` resources, then point the GitHub App's webhook at the integration-controller. Watch the pipeline
with `kubectl get patchy -n patchy` (the shared kubectl category) or `kubectl get findings -w`.

### dev (kind)

Local `patchy/*:dev` images (`make snapshot`, retag, then `kind load docker-image`), a NodePort webhook (30079 — point
your tunnel, `gh webhook forward` or smee.io, at it; map it with `extraPortMappings` in your kind config), minutes
instead of hours (2m accumulation, 2m minimum age, 30m finding TTL), the static-file fake CMDB enhancer mounted from a
ConfigMap, placeholder Integration/Forge CRs, and tiny resource requests. On
[Colima](https://oss.bitwisemedia.uk/patchy/deployment/colima/) the whole snapshot → retag → apply flow is one command,
`make dev-colima`, and no image loading is needed.

Three things to know about dev:

- **The placeholder GitHub credential fails every GitHub call** — ingestion and the CR state machine work (replay
  fixtures with `mise run replay`), but issue projection and repository artifacts error until a real credential arrives.
  The dev shortcut is a PAT: `GITHUB_TOKEN=<pat> make dev-colima` writes it into the `patchy-github` Secret (see
  `overlays/dev/secret-dev.yaml` for the by-hand equivalent).
- **kind runs kindnet, which ignores NetworkPolicy.** The policies apply cleanly and do nothing. A green dev apply is
  not evidence of a working sandbox.
- **The fake harness still needs the `patchy-anthropic` Secret to exist.** `PATCHY_INVESTIGATE_HARNESS=fake` reaches the
  agent pod (the controllers pass their agent configuration through to every Job), so no model is ever called and the
  key's value is irrelevant — but `internal/jobs` wires `ANTHROPIC_API_KEY` from that Secret unconditionally, and a
  missing Secret is a `CreateContainerConfigError`. The dev overlay ships an obvious placeholder.

### prod

DESIGN.md's real intervals (1h accumulation, 1h minimum age, the 14-day finding TTL), the claude harness, the Cilium
FQDN policy, production-sized requests/limits, and **digest-pinned images**. The `sha256:0000…` values in
`overlays/prod/kustomization.yaml` and `PATCHY_AGENT_IMAGE` in `overlays/prod/configmap-patch.yaml` are placeholders —
replace them with the digests your release pipeline published before applying anything. Bring your real Secrets and
`Integration`/`Forge` resources with SOPS or external-secrets.

Ingress/TLS for the webhook endpoint is deliberately absent: add it in an environment overlay, in front of
`patchy-integration-controller:8080`.
