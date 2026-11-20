<!--
Copyright 2026 Bitwise Media Group Ltd.
SPDX-License-Identifier: MIT
-->

# patchy Helm chart

Deploys the patchy stack into the release namespace: the `patchy.bitwisemedia.uk` CRDs, the pipeline controllers
(integration, source, context, investigation, remediation), the status server, the egress broker whenever a claude or
codex runner is enabled, and optionally the evaluation controller. It also deploys the agent sandbox namespace, RBAC,
ConfigMaps, Services, and NetworkPolicies. It is the Helm version of [`deploy/kustomize`](../../deploy/kustomize), with
the same resources, defaults, and isolation model. Every release publishes it to OCI.

```sh
helm install patchy oci://ghcr.io/bitwise-media-group/patchy/charts/patchy \
    --version <X.Y.Z> --namespace patchy --create-namespace
```

The chart version tracks the app release 1:1, and the default image tag is `v<appVersion>`. Installing chart `X.Y.Z`
runs images `vX.Y.Z`.

## Architecture

The custom resources are the state machine. Findings, investigations, and remediations live as CRs in the release
namespace, and the Kubernetes API is the only state store (`kubectl get patchy -n <namespace>` shows the pipeline). The
pipeline controllers split the work:

| Controller                   | Role                                                                                                                                                                                         |
| ---------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **integration-controller**   | The only internet-facing entry point: provider webhook receivers on :8080 (GitHub: `POST /github/webhooks`), scanner-alert ingestion into Findings, tracking-issue projection, human signals |
| **source-controller**        | Forge/Repository reconcilers plus the artifact server on :9790, which serves the SHA-pinned repository tarballs that agent pods fetch without credentials                                    |
| **context-controller**       | The enhancer chain over Opened findings: Google Cloud, AWS and Azure resource lookups, generic HTTP enhancers and a static context file; no GitHub access                                    |
| **investigation-controller** | The gate and the analysis scheduler: launches analysis agent Jobs and applies the verdict's phase transition                                                                                 |
| **remediation-controller**   | Queue admission, the remediation agent Jobs, push/PR, and the rollup/TTL loop                                                                                                                |

Every Deployment runs as a singleton (`replicas: 1` + `Recreate`); the controllers also take a leader-election Lease as
protection during rollouts. They mount their service-account tokens. [`templates/rbac.yaml`](templates/rbac.yaml) lists,
verb by verb, what each identity may do. Services exist only for what something dials: the integration-controller's
:8080 (the webhook), the source-controller's :9790 (the artifact endpoint), and the status server, egress broker and
evaluation controller on :8080. Every other port is a kubelet-probed :8081.

The CRDs render as templates gated by `crds.install`. They live in `templates/crds/` instead of the chart's install-only
`crds/` directory, so `helm upgrade` keeps them current. When `crds.keep` is true, the chart stamps
`helm.sh/resource-policy: keep` on them so uninstall never deletes them. Deleting them would also delete the all-time
FindingRollup statistics.

## Switching the pipeline on

The controllers do nothing until two custom resources exist: an **Integration** (where findings come from, where the
tracking issues go, webhook validation) and a **Forge** (how repositories are cloned and pushed). Those CRs live in the
sibling [`patchy-config`](../patchy-config) chart. Install it **after** this one, into the same namespace. Helm
validates every manifest against the API server before applying anything, so the CRs cannot go in the same first install
as the CRDs they depend on:

```sh
helm install patchy-config oci://ghcr.io/bitwise-media-group/patchy/charts/patchy-config \
    --version <X.Y.Z> --namespace patchy -f values.yaml
```

See the [`patchy-config` README](../patchy-config/README.md) for the values shape. You can also apply the CRs yourself
with `kubectl`. [`deploy/kustomize/base/crs.example.yaml`](../../deploy/kustomize/base/crs.example.yaml) walks through
every field (GHES base URLs, org allowlists, repository regexes).

## Secrets

You create these separately (with SOPS, external-secrets, or `kubectl` for dev). The chart references them and does not
manage them. See [`deploy/kustomize/base/secrets.example.yaml`](../../deploy/kustomize/base/secrets.example.yaml) for
their shapes and one-line commands to create them:

| Secret               | Namespace         | Keys                                                 | What                                                                                                                                                                                                                                                                                                                                                                                                                    |
| -------------------- | ----------------- | ---------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| e.g. `patchy-github` | release namespace | `appID` + `privateKey` (or `token`), `webhookSecret` | The forge/provider credential, named by each Integration/Forge CR's `spec.secretRef`. It is **not** mounted into any Deployment; controllers read it through the API when they need it                                                                                                                                                                                                                                  |
| `patchy-anthropic`   | release namespace | `api-key`                                            | The claude runner's Anthropic credential. ONLY the egress broker uses it, never an agent pod. An API key, or a `claude setup-token` OAuth token with `egressBroker.anthropicAuth: token`. Needed when the claude provider is `anthropic`; bedrock/vertex/foundry-entra use the broker's workload identity instead. **Migration:** it used to live in `patchy-agents`. Create it here, upgrade, then delete the old copy |
| `patchy-openai`      | release namespace | `api-key`                                            | The codex runner's OpenAI platform API key. ONLY the egress broker uses it, never an agent pod. Needed when `agent.runners.codex.enabled: true` with provider `openai` (`egressBroker.openaiSecret`); azure uses `egressBroker.azureSecret` or Entra. A ChatGPT-plan token cannot go through the broker. **Migration:** it used to live in `patchy-agents`. Create it here, upgrade, then delete the old copy           |

One GitHub Secret can serve both CRs, or you can split read and write identities across two GitHub Apps. The provider
has one webhook URL. Point it at `https://<webhook.host>/github/webhooks` and enable one of the chart's entry point
options: `webhook.ingress` (plain Ingress, works anywhere) or `webhook.httpRoute` (Gateway API). Both sit in front of
the **integration-controller**, which validates each delivery against the matching Integration's `webhookSecret`. See
the [webhook exposure docs](../../docs/deployment/webhook.md) for details and per-platform (EKS, AKS, GKE) notes.

## Agent isolation

The agent Jobs run in their own namespace (`agent.namespace`). The chart creates it with the `restricted` Pod Security
labels. `helm uninstall` deletes it, which kills any running agent Job. The isolation model has three layers, most
important first:

1. **No credentials in the pod.** The agent pod holds no forge credential at all, not even in an init container. A
   claude or codex pod holds **no credential of any kind**. Its model traffic goes through the egress credential broker,
   which the chart deploys whenever a claude or codex runner is enabled. The pod authenticates to the broker with an
   audience-bound projected ServiceAccount token. That token proves who the pod is, but grants no access by itself. The
   repository arrives as a digest-verified tarball from the source-controller's in-cluster artifact server (:9790). The
   only Secret in a pod is the per-Job handoff markdown. The agent ServiceAccount has no Role, and its API token is not
   mounted.
2. **NetworkPolicy** (`agent.networkPolicy.create`). All traffic is denied both ways by default. The policy then allows
   only DNS, the artifact server, the broker, and external TCP 443, with `clusterCIDRs` excluded. The 443 rule is the L3
   baseline for a runner that does not use the broker; no shipped runner needs it. All egress from a claude or codex pod
   stays inside the cluster.
3. **Hostname policy** (defence in depth). `agent.networkPolicy.mode` picks the policy type the cluster can enforce.
   Each type renders **one policy per enabled runner**, selecting that harness's pods by their
   `patchy.bitwisemedia.uk/harness` label. Each runner can then reach only its own model API plus the in-cluster
   endpoints. The brokered claude and codex runners have **no external hosts at all**: their Cilium policies are
   cluster-only, and the chart skips their GKE/Istio entries. There are no GitHub hosts, because the pod never talks to
   GitHub.

| `mode`   | renders                                     | requires                                                                     |
| -------- | ------------------------------------------- | ---------------------------------------------------------------------------- |
| `auto`   | whichever of the below the cluster supports | nothing (the default)                                                        |
| `gke`    | `FQDNNetworkPolicy` (networking.gke.io)     | GKE Dataplane V2 created/updated with `--enable-fqdn-network-policy`         |
| `cilium` | `CiliumNetworkPolicy` with `toFQDNs`        | a real Cilium with DNS-based policy (the DNS proxy). **Not GKE** (see below) |
| `istio`  | `Sidecar` (REGISTRY_ONLY) + `ServiceEntry`  | native sidecars (`ENABLE_NATIVE_SIDECARS=true`) + the Istio CNI node agent   |
| `none`   | nothing beyond the base NetworkPolicy       | nothing                                                                      |

**`auto` detection.** Helm resolves `.Capabilities` against the live cluster on every `install`/`upgrade` and every
helm-controller reconcile, so `auto` reads the API surface. It picks `gke` when GKE's `FQDNNetworkPolicy` CRD is
present. That CRD only exists once `--enable-fqdn-network-policy` is on, which makes it a reliable signal. It picks
`cilium` when a real Cilium is present, and `none` otherwise. There are two exclusions:

- It never selects `istio`, because CRD presence says nothing about native sidecars. Without them the agent Job never
  completes.
- It never selects `cilium` on GKE. Dataplane V2 publishes cilium.io CRDs, but it has not applied `CiliumNetworkPolicy`
  since 1.21.5-gke.1300 and rejects every L7 rule. A CNP there enforces nothing, even though `kubectl get` shows it as
  if it protects the pods.

If you set `mode: cilium` explicitly on GKE, the chart trusts it (for a self-managed Cilium without Dataplane V2). An
off-cluster `helm template` sees no cluster and resolves to `none`. Pin `mode` when you need a deterministic render.

**Why the base policy shrinks under an FQDN mode.** Network policies are **additive**: a packet that matches _any_
policy selecting the pod is allowed, and no policy can remove what another allows. Suppose a `CiliumNetworkPolicy` or
`FQDNNetworkPolicy` names `api.anthropic.com`, next to a NetworkPolicy that already allows 443 to `0.0.0.0/0`. The FQDN
policy then restricts nothing, because the union is still "443 to anywhere". So `agent.networkPolicy.broadEgress: auto`
drops that broad rule under `cilium` and `gke`. It keeps the rule under `none` and `istio`, because the sidecar enforces
on SNI in a separate layer and still needs the L3 baseline beneath it.

- Under `cilium`, the chart drops the entire `-agents-egress` policy. The CNP itself grants DNS (through the proxy,
  which learns the resolved addresses), the artifact server, and the model API.
- Under `gke`, the policy stays but without the 443 rule. An `FQDNNetworkPolicy` is egress-only and cannot name a
  resolver or a ClusterIP Service.

Set `broadEgress: always` to keep the broad rule while you test a new mode. If the mode turns out not to enforce, egress
stays open instead of blocking every runner. The chart never drops the namespace default-deny, so a missing CRD leaves
the sandbox closed.

## Values worth knowing

The per-controller blocks (`integrationController`, `sourceController`, `contextController`, `investigationController`,
`remediationController`) all share one shape:

- `<controller>.image`: key-by-key override of the global `image.*` prefix/tag. A `digest` pins that image.
- `<controller>.config`: the `PATCHY_*` keys that binary reads (integration: `accumulationWindow`; investigation:
  `findingMinAge`, `maxConcurrentInvestigations`, `confidenceThreshold`; remediation: `maxConcurrentRemediations`,
  `findingTTL`), rendered into a per-controller ConfigMap. `config.*` holds the shared keys (`logLevel`, `maxAttempts`,
  `priorityAgingInterval`, `priorityAgingCap`). You can override each one per controller by repeating it under
  `<controller>.config`. `config.extra` and `<controller>.config.extra` render any `PATCHY_*` keys and win over anything
  the chart derives.
- `<controller>.serviceAccount` / `networkPolicy`: that controller's identity and L3/L4 policy. `service` exists only on
  the two controllers that other components connect to (integration :8080, source :9790). Use NodePort for kind or local
  development.
- `<controller>.resources`, `podAnnotations`, `podLabels`, `nodeSelector`, `tolerations`, `affinity`: per-controller pod
  tuning.

Settings that several components share stay global:

- `image.*`: repository prefix (registry included), tag (default `v<appVersion>`), pull policy, pull secrets.
- `webhook.*`: the single external entry point (`host`, plus one of `ingress` / `httpRoute`) in front of the
  integration-controller. A provider has one webhook URL, so exposure is set once for the whole chart.
- `agent.*`: the sandbox. It covers the namespace, service account, `jobDeadline`/`jobTTL`, and the limits for the two
  stages:
  - `modelAllowlist`: canonical, provider-qualified model ids or family aliases such as `anthropic/claude-opus`.
  - `investigate.*`: absolute limits.
  - `remediate.*`: `auto.*` is what an unattended fix gets, and an estimate above it needs approval. `manual.*` is the
    most an approval can grant. `model` is the fallback when the report's choice is not on the allowlist.
- `agent.runners.<harness>`: the per-harness runners. Each has `enabled`, the runner `image` (default
  `<prefix>/<harness>-agent-runner`), and a `provider` block that selects which API the egress broker fronts. Pinning
  the image digest here also sets the runner image env var; kustomize needs both set separately. Provider fields:
  - claude: `name` (`anthropic`/`bedrock`/`vertex`/`foundry`), `region`, `regionPrefix`, `projectID`, `resource`,
    `modelMap`, `env`.
  - codex: `name` (`openai`/`azure`), `resource`, `modelMap` (required for azure), `env`.

  Both runners go through the broker and are enabled by configuration alone. The chart ignores the old codex
  `secret`/`secretKey`/`secretEnv`/`hosts`/`dnsPatterns` keys and prints a NOTES warning. The model chosen for a stage
  decides which runner (image + provider + egress policy) the Job uses, so an OpenAI model goes to `codex` and an
  Anthropic model to `claude`. patchy supports first-party coding agents only: each model runs on its vendor's own CLI,
  so a model whose harness is not enabled cannot run.

- `egressBroker.*`: the egress credential broker, deployed whenever a claude or codex runner is enabled. It covers:
  - the credential Secrets (`anthropicSecret` + `anthropicAuth`, `foundrySecret`, `openaiSecret`, `azureSecret`)
  - `ssePingInterval`
  - for bedrock/vertex/entra, `serviceAccount.annotations`/`podLabels`, where the workload identity attaches
  - `networkPolicy.extraEgress`, for reaching the cloud metadata endpoints
- `agent.networkPolicy.*`: the sandbox policies (above).
- `commonLabels` / `commonAnnotations`: stamped on every object the chart renders. Annotations reach the pods too, and
  per-object annotations win key by key.
- `crds.install` / `crds.keep`: CRD lifecycle.

Do not scale the Deployments. Each must run as a single instance, so they hardcode `replicas: 1` with
`strategy: Recreate`. The leader-election Lease protects against a broken rollout. It does not let you scale.

## Publishing

When a release is published, [`.github/workflows/helm.yaml`](../../.github/workflows/helm.yaml) packages `charts/patchy`
(and the sibling `charts/patchy-config`) and pushes them to `oci://ghcr.io/bitwise-media-group/patchy/charts`.
release-please stamps `version`/`appVersion` in each `Chart.yaml` as part of the release PR. Lint locally with
`mise run helm-lint`.
