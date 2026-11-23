<!--
Copyright 2026 Bitwise Media Group Ltd.
SPDX-License-Identifier: MIT
-->

# Deploying patchy

This is the operator guide for the Kubernetes deployment. It covers what runs where, the GitHub App you must register,
and the Secrets and custom resources you must create. It also states plainly what the agent sandbox does and does not
guarantee.

To learn _what the system does_, read [DESIGN.md](../DESIGN.md). To find _where the code lives_, read
[AGENTS.md](../AGENTS.md).

If you prefer Helm, [`charts/patchy`](../charts/patchy/README.md) renders this same stack. It is published to
`oci://ghcr.io/bitwise-media-group/patchy/charts/patchy` on release. The Integration/Forge CRs install separately via
[`charts/patchy-config`](../charts/patchy-config/README.md). Everything below about the App, the Secrets, and the
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

The Dockerfiles live at the repo root. `Dockerfile.controller` builds all controllers (ARG `TARGET`). Each harness has
its own agent image: `Dockerfile.claude-agent-runner` (agent-runner + git + claude CLI) and
`Dockerfile.codex-agent-runner` (agent-runner + git + codex CLI). A Job runs the image of the harness resolved for its
model.

## What gets deployed where

There are two namespaces. The split between them is the security boundary.

| Namespace       | Workload                                                                                                                                                                                                                                                                                                                                                                               | Credentials it holds                                                                                                                  |
| --------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------- |
| `patchy`        | `integration-controller` (the only internet-facing workload), `source-controller`, `context-controller`, `investigation-controller`, `remediation-controller`, `evaluation-controller` (optional; the evaluation API that evolve calls; put your ingress in front of its Service when you use it), `egress-broker` (the reverse proxy all claude and codex model traffic goes through) | reads the GitHub Secret referenced by your Integration/Forge CRs; the broker holds the model credentials (or cloud workload identity) |
| `patchy-agents` | ephemeral agent `Job`s, created at runtime by the three job-launching controllers                                                                                                                                                                                                                                                                                                      | nothing. Claude and codex Jobs carry an identity token only (the model credentials live with the egress broker)                       |

The **evaluation controller** is optional. It runs remote skill evaluations that
[evolve](https://github.com/bitwise-media-group/evolve) submits, using the same agent-Job machinery (see
`docs/configuration/evaluation-controller.md`). It needs:

- the `patchy-evaluation-auth` Secret, which holds the bearer-auth configuration (see `secrets.example.yaml`);
- evolve-runner images pinned in the ConfigMap (`PATCHY_EVOLVE_*_IMAGE`);
- RBAC bindings for submitters (`rbac.users.example.yaml`, `patchy-evaluations-submitter`);
- source-controller's internal blob endpoint (`PATCHY_ARTIFACT_INTERNAL_ADDR: ":9791"`), which a NetworkPolicy opens to
  the evaluation controller only.

To run without it, remove the Deployment and that ConfigMap key. In the Helm chart the feature is off behind
`evaluationController.enabled: false`.

The custom resources in `patchy` **are** the state machine: `Finding`, `Repository`, `Investigation`, `Remediation`,
`FindingRollup`, plus the `Integration`/`Forge` configuration kinds. etcd is the only state store. The CRDs render first
in the base (`base/crds/`, controller-gen output).

Each controller runs as a **single replica** with `strategy: Recreate`. The binaries do run leader election (a
coordination Lease per controller). That guards against a bad rollout racing two replicas. The deployment model is still
one replica per controller, so do not scale these up. The status server and the egress broker hold no state and no
lease, so they use `RollingUpdate` (`maxUnavailable: 0`): the new pod is ready before the old one stops. They still run
one replica.

Every controller talks to the Kubernetes API. So every controller identity mounts its token and gets a verb-by-verb
`Role` in `patchy` (see `base/rbac.yaml`). The three job-launching controllers (investigation, remediation, evaluation)
also share a Role in `patchy-agents` (jobs, pods, pods/log, secrets). The agent Job pods have **no RBAC at all** and run
with `automountServiceAccountToken: false`. A prompt-injected agent must not be able to read the Secrets that the
isolation model depends on.

## Images

GoReleaser builds and publishes all the images (`dockers_v2` in `.goreleaser.yaml`) in every release. Each image is a
multi-arch (`linux/amd64` + `linux/arm64`) manifest pushed to `ghcr.io/bitwise-media-group/patchy/<name>` and tagged
`vX.Y.Z` + `latest`. GoReleaser compiles the binaries once and hands them to `docker buildx`. The repo-root
`Dockerfile.*` only assemble the runtime layer (`COPY $TARGETPLATFORM/<binary>`), so you cannot `docker build` them
directly from the repo. To build images locally, run `make snapshot` (needs docker + buildx). It produces per-arch
`ghcr.io/...:v<next>-snapshot-<sha>-<arch>` images without pushing, ready for `kind load docker-image`. Each release
also uploads `digests.txt` and attests every image digest in it. Verify with
`gh attestation verify --owner bitwise-media-group oci://ghcr.io/bitwise-media-group/patchy/<name>:vX.Y.Z`.

One Dockerfile builds every controller-shaped binary (the controllers, the status server, the egress broker) on the same
`distroless/static` base. The per-image `build_args` in `.goreleaser.yaml` set `TARGET` to pick the binary. Every
controller is pure Go and starts no subprocesses. source-controller downloads repository archives over the GitHub API,
and remediation-controller pushes the agent's changeset through the Git Data API (`internal/ghpush`). So no controller
image carries a `git` binary. Everything runs as uid 65532 with a read-only root filesystem. `/tmp` is an `emptyDir` in
every pod, which keeps the Go runtime's temp-file users working.

The agent image is based on `cgr.dev/chainguard/wolfi-base` (glibc-based, rebuilt continuously against upstream security
fixes). It carries:

- the `claude` CLI as Anthropic's self-contained native binary. The build downloads it from the official release bucket,
  verifies its sha256 against the manifest, and pins it with `ARG CLAUDE_VERSION` (Dependabot/renovate should bump it).
  Its versions match the `@anthropic-ai/claude-code` npm package;
- `git`, `curl`, and `/bin/sh` for the init container's artifact fetch;
- the `agent-runner` binary on `PATH` under that exact name (`internal/jobs` runs `Command: ["agent-runner"]`).

## GitHub App

Register one App for the whole pipeline and install it on the repositories patchy watches.

**Repository permissions:**

| Permission             | Access       | Why                                                                        |
| ---------------------- | ------------ | -------------------------------------------------------------------------- |
| Code scanning alerts   | Read & write | read alert detail; dismiss false positives (DESIGN.md req 6)               |
| Secret scanning alerts | Read & write | read leaked-secret alerts with the value hidden; resolve confident ignores |
| Issues                 | Read & write | the tracking projection: open, label, comment, close                       |
| Contents               | Read & write | download the repository archive; push the remediation branch               |
| Pull requests          | Read & write | open the PR the human reviews                                              |
| Metadata               | Read         | mandatory                                                                  |

**Webhook events to subscribe:** `code_scanning_alert`, `secret_scanning_alert` (when `github.secretScanningAlerts` is
enabled), `issues`, `issue_comment`, `pull_request`.

**Webhook URL:** set exactly one, pointed at the integration-controller: `https://<your-host>/github/webhooks`. The
integration-controller is the single receiver. It validates each delivery against the webhook secrets of your configured
Integrations, ingests scanner events into Findings, and applies the human signals (issue close, `/approve`, PR merge).
No other controller serves a webhook. When those Integrations are configured, the same receiver also serves:

- `/google-cloud/webhooks` (Security Command Center via a Pub/Sub push subscription);
- `/wiz/webhooks` (Wiz automation rules);
- `/generic/<name>/webhooks` (one per generic Integration, under the static `/generic/` prefix).

See their integration docs for details. The base ships a ClusterIP Service and no Ingress. Put your Ingress or Gateway
in front of `patchy-integration-controller:8080` in your own overlay.

GitHub never retries a failed delivery on its own. Enable `spec.github.redelivery` on the Integration and the controller
sweeps the App's delivery log every reconcile interval, redelivering anything that missed. This needs App credentials,
because a PAT cannot see the delivery log. The status page's user menu adds a full replay on demand (`spec.replay`, RBAC
verb `replay`). Some alerts predate webhook coverage entirely, so the delivery log has nothing to replay. The manual
backfill reaches those (`spec.backfill`, verb `backfill`). It is a bounded list-alerts walk that you start from the
status page's configuration view or with `patchy backfill`.

The integration-scoped actions are custom RBAC verbs on `integrations.patchy.bitwisemedia.uk`: `backfill`, `replay`, and
`reset`. The `patchy-integration-actions` admission policy enforces them field by field, in the same way
`patchy-finding-actions` enforces the finding verbs (`base/admission-policy.yaml`). **Migration note (pre-1.0 breaking
change):** `replay` and `reset` used to be verbs on `findings`. They moved to `integrations`, which is the resource they
stamp and the one the admission policy's authorizer checks. Role bindings that grant them on findings must move those
verbs to an `integrations` rule. `base/rbac.users.example.yaml` shows the shape.

Webhooks do **not** drive pipeline progress. The gates ("accumulation closed", "older than an hour", "a free remediation
slot") are conditions that no event can announce. The controllers' watch-driven reconcile loops move the pipeline
forward. The webhook path handles ingestion and human-in-the-loop signals.

## Secrets and custom resources

You need two Secrets, and neither belongs in git. Use SOPS or external-secrets. `base/secrets.example.yaml` is a
commented template, not a resource.

```sh
kubectl -n patchy create secret generic patchy-github \
  --from-literal=appID=123456 \
  --from-file=privateKey=./patchy.private-key.pem \
  --from-literal=webhookSecret="$(openssl rand -hex 32)"

# NOTE the namespace: the claude credential belongs to the egress BROKER in
# the patchy namespace. It never enters an agent pod. (Releases before the
# broker kept it in patchy-agents: create it here, upgrade, delete the old
# copy.)
kubectl -n patchy create secret generic patchy-anthropic \
  --from-literal=api-key="$ANTHROPIC_API_KEY"
```

`patchy-anthropic` is the claude runner's Anthropic credential. Only the egress broker uses it. The broker is the
reverse proxy all claude and codex model traffic goes through (`deployment-egress-broker.yaml`), and its readiness probe
fails while the credential is unusable. Other options:

- `PATCHY_ANTHROPIC_AUTH=token` sends a `claude setup-token` OAuth token as a bearer instead of an API key.
- The bedrock/vertex/foundry providers need no Secret at all. The broker signs with its cloud workload identity
  (annotate the `patchy-egress-broker` ServiceAccount).
- Codex goes through the broker in the same way. To enable it, add `codex` to `PATCHY_HARNESSES` and uncomment
  `PATCHY_OPENAI_API_KEY_FILE`. It then needs `patchy-openai` (an OpenAI platform API key) in **patchy**. The broker
  mounts it as optional, so the Deployment starts without it. For Azure OpenAI, set `PATCHY_CODEX_PROVIDER=azure` and
  the broker's `PATCHY_AZURE_*` keys instead.

No model credential ever enters `patchy-agents`. A fake-harness run (dev) needs no model credential at all.

You then switch the pipeline on with two custom resources that reference that Secret:

- an `Integration` (webhook validation, alert ingestion, issue projection);
- a `Forge` (repository read for the artifact, write for the push + PR).

`base/crs.example.yaml` is the commented template. The dev overlay applies working placeholders
(`overlays/dev/crs-dev.yaml`). No Deployment mounts a GitHub credential. The controllers read the Secret by name through
the API, when they need it.

## Configuration

All configuration is `PATCHY_*` environment in one ConfigMap (`base/configmap.yaml`), consumed with `envFrom`.
`internal/cli/options.go` maps each variable back onto a cobra flag. The prefix is `PATCHY` and dashes become
underscores, so `--claude-agent-image` is `PATCHY_CLAUDE_AGENT_IMAGE`. Precedence is flag > env > default. The
Deployments pass no flags except `serve`, so the ConfigMap is the only place to configure them. A binary ignores any key
it does not bind, which is why one ConfigMap serves every binary.

The per-harness `PATCHY_<HARNESS>_AGENT_IMAGE` keys are a special case. They are the strings the job controllers stamp
into the Jobs they create, and kustomize's `images:` transformer **does not rewrite ConfigMap values**. An overlay that
pins a runner image must patch both the `images:` entry and the matching `PATCHY_<HARNESS>_AGENT_IMAGE` key.

**Egress proxy.** The controllers honour the standard `HTTPS_PROXY`/`HTTP_PROXY`/`NO_PROXY` environment. Some overlays
must reach GitHub through a corporate forward proxy (the GHEC IP-allowlist estate). Such an overlay patches those
variables into the ConfigMap next to the `PATCHY_*` keys. Keep in-cluster traffic out of the proxy. Set at least
`NO_PROXY: localhost,127.0.0.1,.svc,.cluster.local`, so the artifact endpoint, the egress broker, and the API server
stay direct. Also widen the controllers' egress NetworkPolicies for the proxy's port. A `spec.proxy` on a `Forge` or
GitHub `Integration` overrides the environment for that resource's traffic. Proxy basic-auth credentials go in its
credential Secret under `proxyUsername`/`proxyPassword`.

## The isolation model: what it actually is

DESIGN.md requires the coding agent to run with "no internet access / no access to github APIs". For the default
(claude) runner both now hold. Its model traffic goes through the in-cluster egress credential broker, so the pod dials
no external host at all. The isolation has three layers.

**1. No credentials in the pod. This is the main protection.** The agent pod holds **no credential at all**, in any
container. The repository arrives as a tarball from source-controller's in-cluster artifact server. The URL carries an
unguessable 128-bit id, and the Job pins the sha256 digest. The init container verifies the digest before it extracts
the tarball and builds the local git base. A claude pod holds no model key either. The broker injects or signs the
credential outbound. The pod authenticates to the broker with an audience-bound projected ServiceAccount token. That
token proves identity but grants no access by itself. The per-Job Secret carries only handoff markdown. `internal/jobs`
lists `GITHUB_TOKEN` (and every credential channel) in `reservedEnv`, so no configuration can slip a credential in. The
controllers perform all GitHub side effects (issue projection, alert dismissal, branch push, PRs) with short-lived
tokens scoped to one repository. An agent that reaches `github.com` reaches it as an anonymous member of the public.

**2. NetworkPolicy, the minimum layer.** `patchy-agents` is default-deny in both directions. Egress is allowed again
for:

- DNS;
- the artifact port (9790) to source-controller;
- the broker port (8080) to the egress broker. With the two above, this is all the egress a claude or codex pod has, and
  all of it is cluster-local;
- TCP 443, with the cluster's own ranges and the cloud metadata endpoint (169.254.169.254) excluded. This is an L3
  fallback that no shipped runner needs.

**A plain NetworkPolicy is L3/L4 and cannot match a hostname.** So "TCP 443" means every HTTPS host on the internet, not
only the model vendor's. Adjust the `except:` CIDRs in `base/networkpolicy.yaml` to your cluster's pod/service/node
CIDRs.

**3. Hostname policy, an extra layer where the infrastructure supports it.** Add exactly one component. Each one narrows
that egress to the model hosts of the non-brokered runners and nothing else external. The brokered claude and codex
runners need no entry, because they have no external hosts. So today the components' practical job is to remove the
broad 443 rule. No GitHub hosts appear in the agent's allowlist at all, because the pod never talks to a forge. The FQDN
policy is not the boundary. The missing credential is the boundary.

- `components/cilium` (enabled by the prod overlay) adds a `CiliumNetworkPolicy` with `toFQDNs`, plus a DNS rule that
  limits which names the pod may resolve at all. It requires Cilium with the DNS proxy.
- `components/gke-fqdn` is for GKE Dataplane V2. That is Cilium underneath, but it has not honoured the
  `CiliumNetworkPolicy` CRD since 1.21.5-gke.1300 and rejects every L7 rule, so the cilium component does nothing there.
  Every shipped runner goes through the broker, so this component renders no `FQDNNetworkPolicy`
  (`networking.gke.io/v1alpha1`). A non-brokered runner would get one, and the cluster would then need
  `--enable-fqdn-network-policy`. Today the component only removes the base policy's broad 443 rule. A JSON-patch `test`
  guards that removal, so a reordered base fails the build instead of removing the broker rule. It cannot express DNS or
  a ClusterIP destination, so both stay with the base policy, and DNS exfiltration stays open.
- `components/istio` adds a `Sidecar` with `REGISTRY_ONLY`, matched by SNI. It exposes only that runner's model
  ServiceEntry and the `patchy` namespace's Services (which cover the artifact server and the broker). Brokered claude
  and codex need no ServiceEntry, so the component ships none. It requires native sidecars (Kubernetes ≥ 1.29, istiod
  with `ENABLE_NATIVE_SIDECARS=true`; a classic sidecar hangs the Job). It also requires the Istio CNI node agent,
  because `patchy-agents` enforces the `restricted` Pod Security Standard, which rejects `istio-init`. It differs from
  Cilium in two ways. The proxy does not limit which names the pod may resolve, so DNS exfiltration stays open. And
  enforcement lives inside the pod, not on the node.

The cilium and gke-fqdn components also patch the base policy. cilium deletes `patchy-agents-egress`, and gke-fqdn
removes its broad 443 rule. This patch is required for the allowlist to work. Network policies are **additive**, so an
FQDN allowlist next to a rule that already permits 443 to `0.0.0.0/0` constrains nothing at all. The namespace
default-deny survives either patch, so a missing CRD fails the sandbox closed.

If you have none of the three, drop the components. The base policy still applies and is then your only L3/L4 control.

## Applying

```sh
# Render and review first. Both overlays must render clean.
kubectl kustomize deploy/kustomize/overlays/dev
kubectl kustomize deploy/kustomize/overlays/prod

kubectl apply -k deploy/kustomize/overlays/dev
```

For a fresh cluster, follow this order:

1. Apply the overlay (CRDs render first).
2. Create the two Secrets.
3. Apply your `Integration`/`Forge` resources.
4. Point the GitHub App's webhook at the integration-controller.

Watch the pipeline with `kubectl get patchy -n patchy` (the shared kubectl category) or `kubectl get findings -w`.

### dev (kind)

The dev overlay gives you:

- local `patchy/*:dev` images (`make snapshot`, retag, then `kind load docker-image`);
- a NodePort webhook on 30079. Point your tunnel (`gh webhook forward` or smee.io) at it, and map it with
  `extraPortMappings` in your kind config;
- minutes instead of hours (2m accumulation, 2m minimum age, 30m finding TTL);
- the static-file fake CMDB enhancer, mounted from a ConfigMap;
- placeholder Integration/Forge CRs;
- tiny resource requests.

On [Colima](https://oss.bitwisemedia.uk/patchy/deployment/colima/) one command, `make dev-colima`, runs the whole
snapshot → retag → apply flow, and you do not need to load images.

Three things to know about dev:

- **The placeholder GitHub credential fails every GitHub call.** Ingestion and the CR state machine work (replay
  fixtures with `mise run replay`). Issue projection and repository artifacts fail until a real credential arrives. The
  dev shortcut is a PAT: `GITHUB_TOKEN=<pat> make dev-colima` writes it into the `patchy-github` Secret (see
  `overlays/dev/secret-dev.yaml` for how to do it by hand).
- **kind runs kindnet, which ignores NetworkPolicy.** The policies apply cleanly and do nothing. A green dev apply does
  not show that the sandbox works.
- **The fake harness needs no model credential.** The dev overlay sets `PATCHY_HARNESSES: fake`, which limits the
  enabled set to the fake runner. That runner carries no Secret, so `internal/jobs` wires no model key into its Jobs and
  dev runs with zero real credentials. The claude/codex runners configured in the base are not enabled.

### prod

The prod overlay gives you:

- DESIGN.md's real intervals (1h accumulation, 1h minimum age, the 14-day finding TTL);
- the claude harness. To add codex, enable its runner, set `PATCHY_OPENAI_API_KEY_FILE`, and create `patchy-openai` in
  `patchy`;
- the Cilium FQDN policies;
- production-sized requests/limits;
- **release-pinned images**. Every image in `overlays/prod/kustomization.yaml`, and `PATCHY_CLAUDE_AGENT_IMAGE` in
  `overlays/prod/configmap-patch.yaml`, names the `vX.Y.Z` tag of the release they ship in. release-please bumps them in
  each release PR, so apply the overlay from a release tag. For immutable pins, add a `digest:` per image in your own
  overlay. A digest takes precedence over the tag.

Bring your real Secrets and `Integration`/`Forge` resources with SOPS or external-secrets.

The overlays do not include Ingress/TLS for the webhook endpoint. Add it in an environment overlay, in front of
`patchy-integration-controller:8080`.
