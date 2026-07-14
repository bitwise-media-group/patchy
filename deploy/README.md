<!--
Copyright 2026 Bitwise Media Group Ltd.
SPDX-License-Identifier: MIT
-->

# Deploying patchy

Operator documentation for the Kubernetes deployment: what runs where, the GitHub App you must register, the Secrets you
must create, and an honest account of what the agent sandbox does and does not guarantee.

For _what the system does_, read [DESIGN.md](../DESIGN.md); for _where the code lives_, read [AGENTS.md](../AGENTS.md).

Prefer Helm? [`helm/chart`](../helm/chart/README.md) renders this same stack (published to
`oci://ghcr.io/bitwise-media-group/patchy/charts/patchy` on release); everything below about the App, the Secrets, and
the sandbox applies to both.

## Layout

```text
deploy/
├── kustomize/
│   ├── base/                        # namespaces, RBAC, config, deployments, services, netpol
│   ├── components/cilium/           # optional: FQDN egress policy for the agent sandbox (Cilium CNI)
│   ├── components/istio/            # optional: the same allowlist as a Sidecar + ServiceEntries (Istio mesh)
│   └── overlays/{dev,prod}/
└── README.md
```

The Dockerfiles live at the repo root: `Dockerfile.controller` (all four controllers, ARG `TARGET`) and
`Dockerfile.agent-runner` (the agent image: agent-runner + git + claude CLI).

## What gets deployed where

Two namespaces, and the split between them is the security boundary.

| Namespace       | Workload                                                               | Credentials it holds                                                |
| --------------- | ---------------------------------------------------------------------- | ------------------------------------------------------------------- |
| `patchy`        | `source-controller`, `context-controller`, `remediation-controller`    | GitHub App key, webhook HMAC secret                                 |
| `patchy`        | `webhook-controller` (the only internet-facing workload)               | webhook HMAC secret only — nothing that can mint a GitHub token     |
| `patchy-agents` | ephemeral agent `Job`s, created at runtime by `remediation-controller` | the model API key — and, in the init container only, a scoped token |

Each controller is a **single replica** with `strategy: Recreate`. There is no leader election in the binaries: the
state machine is GitHub issue labels, and two replicas would race to move the same issue (two issues opened for one
advisory, a double-commented enhancement, two agent Jobs for one attempt). Singleton-by-construction is the mechanism —
do not scale these up.

Only `remediation-controller` talks to the Kubernetes API. It gets a `Role` in `patchy-agents` (jobs, pods, pods/log,
secrets, configmaps) and nothing else. The other two controllers and the agent Job pods have **no RBAC at all** and run
with `automountServiceAccountToken: false`.

## Images

All four images are built and published by GoReleaser (`dockers_v2` in `.goreleaser.yaml`) as part of every release:
multi-arch (`linux/amd64` + `linux/arm64`) manifests pushed to `ghcr.io/bitwise-media-group/patchy/<name>` and tagged
`vX.Y.Z` + `latest`. GoReleaser compiles the binaries once and hands them to `docker buildx`; the repo-root
`Dockerfile.*` only assemble the runtime layer (`COPY $TARGETPLATFORM/<binary>`), so they cannot be `docker build`
directly from the repo. To build images locally, run `make snapshot` (needs docker + buildx) — it produces per-arch
`ghcr.io/...:v<next>-snapshot-<sha>-<arch>` images without pushing, ready for `kind load docker-image`. Each release
also uploads `digests.txt` and attests every image digest in it; verify with
`gh attestation verify --owner bitwise-media-group oci://ghcr.io/bitwise-media-group/patchy/<name>:vX.Y.Z`.

One Dockerfile builds all four controllers on the same `distroless/static` base; the per-image `build_args` in
`.goreleaser.yaml` set `TARGET` to pick the binary. Every controller is pure Go with no subprocesses — the
remediation-controller pushes the agent's changeset through the GitHub API (`internal/ghpush`), so no image carries a
`git` binary. Everything runs as uid 65532 with a read-only root filesystem; `/tmp` is an `emptyDir` in every pod, which
is what keeps the Go runtime's temp-file users working.

The agent image is `node:22-slim` because it must carry the `claude` CLI (`@anthropic-ai/claude-code`, pinned by
`ARG CLAUDE_VERSION` — Dependabot/renovate should bump it), plus `git` and `/bin/sh` for the init container's clone, and
the `agent-runner` binary on `PATH` under exactly that name (`internal/jobs` runs `Command: ["agent-runner"]`).

## GitHub App

Register one App for the whole pipeline and install it on the repositories patchy watches.

**Repository permissions:**

| Permission           | Access       | Why                                                          |
| -------------------- | ------------ | ------------------------------------------------------------ |
| Code scanning alerts | Read & write | read alert detail; dismiss false positives (DESIGN.md req 6) |
| Issues               | Read & write | the state machine — open, label, comment, assign, close      |
| Contents             | Read & write | clone the repo; push the remediation branch                  |
| Pull requests        | Read & write | open the PR the human reviews                                |
| Metadata             | Read         | mandatory                                                    |

**Webhook events to subscribe:** `code_scanning_alert`, `issues`, `issue_comment`, `pull_request`.

**Webhook URL — exactly one, pointed at the webhook-controller.** Each binary runs its own `internal/webhook` server on
`:8080` (`POST /webhook`, `GET /healthz`, `GET /readyz`) and ignores the events it does not care about:

| Component                | Path       | Consumes                                                                 |
| ------------------------ | ---------- | ------------------------------------------------------------------------ |
| `webhook-controller`     | `/webhook` | everything — validates the HMAC, routes each event type to its consumers |
| `source-controller`      | `/webhook` | `code_scanning_alert`                                                    |
| `context-controller`     | `/webhook` | `issues`                                                                 |
| `remediation-controller` | `/webhook` | `issue_comment` (`/approve`), `pull_request` (close the issue on merge)  |

A GitHub App supports one webhook URL, so the webhook-controller is the router: point the App at it and each delivery
reaches the controllers that consume its event type (`PATCHY_FORWARD_ROUTES`; the `*` route sends unclaimed event types
to the source-controller, which owns the `pkg/source` plugin seam), signature intact — all four validate the **same**
HMAC secret. It holds no GitHub credential — it is the only component meant to face the internet. The base ships
ClusterIP Services and no Ingress: put your Ingress or Gateway in front of `patchy-webhook-controller` in your own
overlay.

Note that remediation pickup is **not** webhook-driven — the gate is "older than an hour", which no event can announce.
The reconcile loop (`PATCHY_RECONCILE_INTERVAL`) drives it. The webhook path is the human-in-the-loop one.

## Secrets

Three, none of them in git. Use SOPS or external-secrets; `base/secrets.example.yaml` is a commented template, not a
resource.

```sh
kubectl -n patchy create secret generic patchy-github-app \
  --from-literal=app-id=123456 \
  --from-file=private-key.pem=./patchy.private-key.pem

kubectl -n patchy create secret generic patchy-webhook-secret \
  --from-literal=secret="$(openssl rand -hex 32)"

# NOTE the namespace: the model key belongs to the AGENTS, not the controllers.
kubectl -n patchy-agents create secret generic patchy-anthropic \
  --from-literal=api-key="$ANTHROPIC_API_KEY"
```

`patchy-anthropic` must exist even for a fake-harness run: `internal/jobs` unconditionally wires `ANTHROPIC_API_KEY`
into the agent container from it, and a missing Secret means `CreateContainerConfigError` on every Job.

The dev overlay ships placeholder values for all three so a `kubectl apply -k` produces pods that start. They are fake;
overwrite them in-cluster.

## Configuration

Everything is `PATCHY_*` environment in one ConfigMap (`base/configmap.yaml`), consumed with `envFrom`.
`internal/cli/options.go` maps each variable back onto a cobra flag — prefix `PATCHY`, dashes become underscores, so
`--agent-image` is `PATCHY_AGENT_IMAGE` — with precedence flag > env > default. The Deployments pass no flags but
`serve`, so the ConfigMap is the whole configuration surface. A key a binary does not bind is inert, which is why one
ConfigMap serves all three.

`PATCHY_AGENT_IMAGE` is a special case: it is the string the controller stamps into the Job it creates, and kustomize's
`images:` transformer **does not rewrite ConfigMap values**. An overlay that pins the agent-runner image must patch both
the `images:` entry and this key.

## The isolation model — what it actually is

DESIGN.md requires the coding agent to run with "no internet access / no access to github APIs". Taken literally that is
unachievable: `claude -p` **is** a network client of `api.anthropic.com`. What is actually delivered:

**1. Credential absence — the real control.** The agent container has no GitHub credential. `internal/jobs` puts the
short-lived scoped token in the init container's environment only, assembles the auth header inside the shell so the
token never appears in the container command or any config that persists into the working tree the agent sees, and lists
`GITHUB_TOKEN` in `reservedEnv` so no configuration can smuggle one in. All GitHub side effects — labels, comments,
alert dismissal, branch push, PRs — are performed by the remediation-controller, which receives the agent's work as a
structured changeset over stdout. An agent that reaches `github.com` reaches it as an anonymous member of the public.

**2. NetworkPolicy — the floor.** `patchy-agents` is default-deny in both directions. Egress is re-permitted for DNS and
TCP 443 only, with the cluster's own ranges and the cloud metadata endpoint (169.254.169.254) excluded. **A plain
NetworkPolicy is L3/L4 and cannot match a hostname**, so "TCP 443" means every HTTPS host on the internet, not just
Anthropic's. Adjust the `except:` CIDRs in `base/networkpolicy.yaml` to your cluster's pod/service/node CIDRs.

**3. CiliumNetworkPolicy — defence in depth, where the CNI supports it.** `components/cilium/networkpolicy-cilium.yaml`
(enabled by the prod overlay) narrows that egress to `toFQDNs`: `api.anthropic.com` for the agent, and `github.com` /
`codeload.github.com` / `objects.githubusercontent.com` for the init container's clone. GitHub egress is needed **only**
by the init container — but a network policy selects pods, not containers, and both containers share one network
namespace, so the agent container inherits that reach. That is acceptable precisely because of (1): it has nothing to
authenticate with. Do not mistake the FQDN policy for the boundary; the missing credential is the boundary.

On an Istio mesh, `components/istio` delivers the same allowlist instead: a `Sidecar` with `REGISTRY_ONLY` plus
`ServiceEntry`s for the same four hosts, matched by SNI. It requires native sidecars (Kubernetes ≥ 1.29, istiod with
`ENABLE_NATIVE_SIDECARS=true` — a classic sidecar hangs the Job and breaks the init container's clone) and the Istio CNI
node agent (`patchy-agents` enforces the `restricted` Pod Security Standard, which rejects `istio-init`). Two
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

### dev (kind)

Local `patchy/*:dev` images (`make snapshot`, retag, then `kind load docker-image`), NodePort Services (30079
webhook-controller — point your tunnel, `gh webhook forward` or smee.io, at this one; 30080 source, 30081 context, 30082
remediation for hitting a controller directly — map them with `extraPortMappings` in your kind config), minutes instead
of hours (2m accumulation, 2m pickup, 10s reconcile), the static-file fake CMDB enhancer mounted from a ConfigMap, and
tiny resource requests with no limits.

Two things to know about dev:

- **kind runs kindnet, which ignores NetworkPolicy.** The policies apply cleanly and do nothing. A green dev apply is
  not evidence of a working sandbox.
- **The fake harness still needs the `patchy-anthropic` Secret to exist.** `PATCHY_CLASSIFY_HARNESS=fake` reaches the
  agent pod (the controller passes its agent configuration through to every Job), so no model is ever called and the
  key's value is irrelevant — but `internal/jobs` wires `ANTHROPIC_API_KEY` from that Secret unconditionally, and a
  missing Secret is a `CreateContainerConfigError`. The dev overlay ships an obvious placeholder.

### prod

DESIGN.md's real intervals (1h accumulation, 1h pickup, 60s reconcile), the claude harness, the Cilium FQDN policy,
production-sized requests/limits, and **digest-pinned images**. The `sha256:0000…` values in
`overlays/prod/kustomization.yaml` and `PATCHY_AGENT_IMAGE` in `overlays/prod/configmap-patch.yaml` are placeholders —
replace them with the digests your release pipeline published before applying anything.

Ingress/TLS for the webhook endpoints is deliberately absent: add it in an environment overlay, and remember to keep the
same HMAC secret on every route.
