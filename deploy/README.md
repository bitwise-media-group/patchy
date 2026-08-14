<!--
Copyright 2026 Bitwise Media Group Ltd.
SPDX-License-Identifier: MIT
-->

# Deploying patchy

Operator documentation for the Kubernetes deployment: what runs where, the GitHub App you must register, the Secrets and
custom resources you must create, and an honest account of what the agent sandbox does and does not guarantee.

For _what the system does_, read [DESIGN.md](../DESIGN.md); for _where the code lives_, read [AGENTS.md](../AGENTS.md).

Prefer Helm? [`charts/patchy`](../charts/patchy/README.md) renders this same stack (published to
`oci://ghcr.io/bitwise-media-group/patchy/charts/patchy` on release; the Integration/Forge CRs install separately via
[`charts/patchy-config`](../charts/patchy-config/README.md)); everything below about the App, the Secrets, and the
sandbox applies to both.

## Layout

```text
deploy/
├── kustomize/
│   ├── base/                        # CRDs, namespaces, RBAC, config, deployments, services, netpol
│   ├── components/cilium/           # optional: FQDN egress policy for the agent sandbox (Cilium CNI)
│   ├── components/gke-fqdn/         # optional: the same allowlist as an FQDNNetworkPolicy (GKE Dataplane V2)
│   ├── components/istio/            # optional: the same allowlist as a Sidecar + ServiceEntry (Istio mesh)
│   └── overlays/{dev,prod}/
└── README.md
```

The Dockerfiles live at the repo root: `Dockerfile.controller` (all controllers, ARG `TARGET`) and one per-harness agent
image — `Dockerfile.claude-agent-runner` (agent-runner + git + claude CLI), `Dockerfile.codex-agent-runner`
(agent-runner + git + codex CLI) and `Dockerfile.copilot-agent-runner` (agent-runner + git + copilot CLI). A Job runs
the image of the harness resolved for its model.

## What gets deployed where

Two namespaces, and the split between them is the security boundary.

| Namespace       | Workload                                                                                                                                                                                                                                                                                                                                                       | Credentials it holds                                                                                                                        |
| --------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------- |
| `patchy`        | `integration-controller` (the only internet-facing workload), `source-controller`, `context-controller`, `investigation-controller`, `remediation-controller`, `evaluation-controller` (optional; the evolve-facing evaluation API — front its Service with your ingress when used), `egress-broker` (the reverse proxy all claude model traffic goes through) | reads the GitHub Secret referenced by your Integration/Forge CRs; the broker holds the claude model credential (or cloud workload identity) |
| `patchy-agents` | ephemeral agent `Job`s, created at runtime by the three job-launching controllers                                                                                                                                                                                                                                                                              | nothing for claude Jobs (an identity token only); a non-brokered codex/copilot Job carries its one model key                                |

The **evaluation controller** is optional: it executes remote skill evaluations submitted by
[evolve](https://github.com/bitwise-media-group/evolve) through the same agent-Job machinery (see
`docs/configuration/evaluation-controller.md`). It needs the `patchy-evaluation-auth` Secret (bearer-auth configuration
— `secrets.example.yaml`), evolve-runner images pinned in the ConfigMap (`PATCHY_EVOLVE_*_IMAGE`), RBAC bindings for
submitters (`rbac.users.example.yaml`, `patchy-evaluations-submitter`), and source-controller's internal blob endpoint
(`PATCHY_ARTIFACT_INTERNAL_ADDR: ":9791"`, NetworkPolicy-gated to the evaluation controller). Remove the Deployment (and
that ConfigMap key) to run without it; in the Helm chart the whole feature sits behind
`evaluationController.enabled: false`.

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

All the images are built and published by GoReleaser (`dockers_v2` in `.goreleaser.yaml`) as part of every release:
multi-arch (`linux/amd64` + `linux/arm64`) manifests pushed to `ghcr.io/bitwise-media-group/patchy/<name>` and tagged
`vX.Y.Z` + `latest`. GoReleaser compiles the binaries once and hands them to `docker buildx`; the repo-root
`Dockerfile.*` only assemble the runtime layer (`COPY $TARGETPLATFORM/<binary>`), so they cannot be `docker build`
directly from the repo. To build images locally, run `make snapshot` (needs docker + buildx) — it produces per-arch
`ghcr.io/...:v<next>-snapshot-<sha>-<arch>` images without pushing, ready for `kind load docker-image`. Each release
also uploads `digests.txt` and attests every image digest in it; verify with
`gh attestation verify --owner bitwise-media-group oci://ghcr.io/bitwise-media-group/patchy/<name>:vX.Y.Z`.

One Dockerfile builds every controller-shaped binary — the controllers, the status server, the egress broker — on the
same `distroless/static` base; the per-image `build_args` in `.goreleaser.yaml` set `TARGET` to pick the binary. Every
controller is pure Go with no subprocesses — source-controller downloads repository archives over the GitHub API and
remediation-controller pushes the agent's changeset through the Git Data API (`internal/ghpush`), so no controller image
carries a `git` binary. Everything runs as uid 65532 with a read-only root filesystem; `/tmp` is an `emptyDir` in every
pod, which is what keeps the Go runtime's temp-file users working.

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
No other controller serves a webhook. The same receiver serves `/google-cloud/webhooks` (Security Command Center via a
Pub/Sub push subscription), `/wiz/webhooks` (Wiz automation rules), and `/generic/<name>/webhooks` (one per generic
Integration, under the static `/generic/` prefix) when those Integrations are configured — see their integration docs.
The base ships a ClusterIP Service and no Ingress: put your Ingress or Gateway in front of
`patchy-integration-controller:8080` in your own overlay.

GitHub never retries a failed delivery on its own; enable `spec.github.redelivery` on the Integration and the controller
sweeps the App's delivery log every reconcile interval, redelivering anything that missed (App credentials required —
the delivery log is invisible to a PAT). The status page's user menu adds a full replay on demand (`spec.replay`, RBAC
verb `replay`), and alerts that predate webhook coverage entirely — nothing in the delivery log to replay — are reached
by the manual backfill (`spec.backfill`, verb `backfill`), a bounded list-alerts walk triggered from the status page's
configuration view or `patchy backfill`.

The integration-scoped actions are custom RBAC verbs on `integrations.patchy.bitwisemedia.uk` — `backfill`, `replay`,
`reset` — enforced field-by-field by the `patchy-integration-actions` admission policy exactly as the finding verbs are
by `patchy-finding-actions` (`base/admission-policy.yaml`). **Migration note (pre-1.0 breaking change):** `replay` and
`reset` used to be verbs on `findings`; they moved to `integrations`, the resource they actually stamp and the one the
admission policy's authorizer checks. Role bindings that grant them on findings must move those verbs to an
`integrations` rule — `base/rbac.users.example.yaml` shows the shape.

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

# NOTE the namespace: the claude credential belongs to the egress BROKER in
# the patchy namespace — it never enters an agent pod. (Releases before the
# broker kept it in patchy-agents: create it here, upgrade, delete the old
# copy.)
kubectl -n patchy create secret generic patchy-anthropic \
  --from-literal=api-key="$ANTHROPIC_API_KEY"
```

`patchy-anthropic` is the claude runner's Anthropic credential, consumed only by the egress broker — the reverse proxy
all claude model traffic goes through (`deployment-egress-broker.yaml`); its readiness probe fails while the credential
is unusable. `PATCHY_ANTHROPIC_AUTH=token` sends a `claude setup-token` OAuth token as a bearer instead of an API key,
and the bedrock/vertex/foundry providers need no Secret at all — the broker signs with its cloud workload identity
(annotate the `patchy-egress-broker` ServiceAccount). Enable the codex runner and it needs `patchy-openai` (an OpenAI
key) in **patchy-agents**, wired into the pod by `internal/jobs`, and the controllers refuse to start if an enabled
non-brokered harness's credential is missing; the copilot runner needs `patchy-copilot`, which holds a **GitHub token**
rather than a model API key — the one credential class no other agent pod carries, so enable that runner deliberately
and scope the token to Copilot alone. A fake-harness run (dev) needs no model credential at all.

The pipeline is then switched on with two custom resources referencing that Secret — an `Integration` (webhook
validation, alert ingestion, issue projection) and a `Forge` (repository read for the artifact, write for the push +
PR). `base/crs.example.yaml` is the commented template; the dev overlay applies working placeholders
(`overlays/dev/crs-dev.yaml`). No Deployment mounts a GitHub credential: the controllers read the Secret through the
API, on demand, by name.

## Configuration

Everything is `PATCHY_*` environment in one ConfigMap (`base/configmap.yaml`), consumed with `envFrom`.
`internal/cli/options.go` maps each variable back onto a cobra flag — prefix `PATCHY`, dashes become underscores, so
`--claude-agent-image` is `PATCHY_CLAUDE_AGENT_IMAGE` — with precedence flag > env > default. The Deployments pass no
flags but `serve`, so the ConfigMap is the whole configuration surface. A key a binary does not bind is inert, which is
why one ConfigMap serves all five.

The per-harness `PATCHY_<HARNESS>_AGENT_IMAGE` keys are a special case: they are the strings the job controllers stamp
into the Jobs they create, and kustomize's `images:` transformer **does not rewrite ConfigMap values**. An overlay that
pins a runner image must patch both the `images:` entry and the matching `PATCHY_<HARNESS>_AGENT_IMAGE` key.

## The isolation model — what it actually is

DESIGN.md requires the coding agent to run with "no internet access / no access to github APIs". For the default
(claude) runner both now hold: its model traffic goes through the in-cluster egress credential broker, so the pod dials
no external host at all. What is delivered:

**1. Credential absence — the real control.** The agent pod holds **no credential at all**, in any container. The
repository arrives as a tarball from source-controller's in-cluster artifact server: the URL carries an unguessable
128-bit id, the Job pins the sha256 digest, and the init container verifies it before extracting and synthesizing the
local git base. A claude pod holds no model key either — the broker injects or signs the credential outbound, and the
pod authenticates to it with an audience-bound projected ServiceAccount token, an identity document rather than a
capability. The per-Job Secret carries only handoff markdown; `internal/jobs` lists `GITHUB_TOKEN` (and every credential
channel) in `reservedEnv` so no configuration can smuggle a credential in. All GitHub side effects — issue projection,
alert dismissal, branch push, PRs — are performed controller-side with short-lived, per-repository scoped tokens. An
agent that reaches `github.com` reaches it as an anonymous member of the public.

**2. NetworkPolicy — the floor.** `patchy-agents` is default-deny in both directions. Egress is re-permitted for DNS,
the artifact port (9790) to source-controller, the broker port (8080) to the egress broker — the whole of a claude pod's
egress, all cluster-local — and TCP 443 (the non-brokered codex/copilot runners' model APIs) with the cluster's own
ranges and the cloud metadata endpoint (169.254.169.254) excluded. **A plain NetworkPolicy is L3/L4 and cannot match a
hostname**, so "TCP 443" means every HTTPS host on the internet, not just the model vendor's. Adjust the `except:` CIDRs
in `base/networkpolicy.yaml` to your cluster's pod/service/node CIDRs.

**3. Hostname policy — defence in depth, where the infrastructure supports it.** Add exactly one component; each narrows
that egress to the non-brokered runners' model hosts and nothing else external (brokered claude needs no entry — it has
no external hosts). No GitHub hosts appear in the agent's allowlist at all, because the pod never talks to a forge. Do
not mistake the FQDN policy for the boundary; the missing credential is the boundary.

- `components/cilium` (enabled by the prod overlay) — a `CiliumNetworkPolicy` with `toFQDNs`, plus a DNS rule bounding
  what names the pod may resolve at all. Requires Cilium with the DNS proxy.
- `components/gke-fqdn` — an `FQDNNetworkPolicy` (`networking.gke.io/v1alpha1`) for GKE Dataplane V2, which is Cilium
  underneath but has not honoured the `CiliumNetworkPolicy` CRD since 1.21.5-gke.1300 and rejects every L7 rule; the
  cilium component is inert there. Requires the cluster to carry `--enable-fqdn-network-policy`. It cannot express DNS
  or a ClusterIP destination, so both stay with the base policy — and DNS exfiltration stays open.
- `components/istio` — a `Sidecar` with `REGISTRY_ONLY` (exposing only that runner's model ServiceEntry — none for
  brokered claude — and the `patchy` namespace's Services, covering the artifact server and the broker), matched by SNI.
  Requires native sidecars (Kubernetes ≥ 1.29, istiod with `ENABLE_NATIVE_SIDECARS=true` — a classic sidecar hangs the
  Job) and the Istio CNI node agent (`patchy-agents` enforces the `restricted` Pod Security Standard, which rejects
  `istio-init`). Two differences from Cilium: the proxy does not constrain what names the pod may resolve, so DNS
  exfiltration stays open; and enforcement lives inside the pod rather than on the node.

The cilium and gke-fqdn components also patch the base policy — deleting `patchy-agents-egress` and removing its broad
443 rule respectively. That is load-bearing, not tidiness: network policies are **additive**, so an FQDN allowlist
sitting next to a rule that already permits 443 to `0.0.0.0/0` constrains nothing at all. The namespace default-deny
survives either patch, so a missing CRD fails the sandbox closed.

If you have none of the three, drop the components. The base policy still applies and is then the whole of the L3/L4
story.

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
- **The fake harness needs no model credential.** The dev overlay sets `PATCHY_HARNESSES: fake`, which restricts the
  enabled set to the fake runner — it carries no Secret, so `internal/jobs` wires no model key into its Jobs and dev
  runs with zero real credentials. The claude/codex/copilot runners configured in the base are simply not enabled.

### prod

DESIGN.md's real intervals (1h accumulation, 1h minimum age, the 14-day finding TTL), the claude harness (add codex by
enabling its runner and supplying `patchy-openai`, or copilot with `patchy-copilot`), the Cilium FQDN policies,
production-sized requests/limits, and **digest-pinned images**. The `sha256:0000…` values in
`overlays/prod/kustomization.yaml` and `PATCHY_CLAUDE_AGENT_IMAGE` in `overlays/prod/configmap-patch.yaml` are
placeholders — replace them with the digests your release pipeline published before applying anything. Bring your real
Secrets and `Integration`/`Forge` resources with SOPS or external-secrets.

Ingress/TLS for the webhook endpoint is deliberately absent: add it in an environment overlay, in front of
`patchy-integration-controller:8080`.
