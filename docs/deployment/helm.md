# Helm charts

The `patchy` chart (in-repo at `charts/patchy`) renders the full stack. It includes the `patchy.bitwisemedia.uk` CRDs,
the agent namespace with its RBAC and sandbox policies, and one singleton Deployment per component. Each Deployment has
its own ConfigMap, ServiceAccount and NetworkPolicy. The components are:

- the pipeline controllers: integration-controller, source-controller, context-controller, investigation-controller and
  remediation-controller;
- the [status-server](../configuration/status-server.md) (on by default, `statusServer.enabled`);
- the [egress-broker](../configuration/egress-broker.md), whenever a claude or codex runner is enabled;
- the optional [evaluation-controller](../configuration/evaluation-controller.md) (`evaluationController.enabled`, off
  by default).

Services exist for the components that other components call: integration-controller (`:8080`, the webhook),
source-controller (`:9790`, the artifact endpoint), status-server, egress-broker and evaluation-controller. With the
status server on, the integration-controller's Service also carries `:9792`, the in-cluster
[repository-access endpoint](../configuration/integration-controller.md#the-repository-access-endpoint) that only the
status server may call. The chart then also grants the integration-controller `create` on `tokenreviews`, and mounts a
projected token in the status server to call it with.

The companion `patchy-config` chart (`charts/patchy-config`) renders the `Integration`/`Forge` custom resources. It is a
separate chart because Helm validates every manifest against the API server before applying anything. So the CRs cannot
go in the same first install as the CRDs they depend on.

Every release publishes both charts to `oci://ghcr.io/bitwise-media-group/patchy/charts/`. release-please sets `version`
and `appVersion` to match the app version, and the default image tag is `v<appVersion>`. So chart `X.Y.Z` runs images
`vX.Y.Z`.

```sh
helm install patchy oci://ghcr.io/bitwise-media-group/patchy/charts/patchy \
  --version <X.Y.Z> --namespace patchy --create-namespace
helm install patchy-config oci://ghcr.io/bitwise-media-group/patchy/charts/patchy-config \
  --version <X.Y.Z> --namespace patchy -f values.yaml
```

The chart requires Kubernetes ≥ 1.34 (the oldest line not yet end-of-life). It references the
[two Secrets](../getting-started/install.md#create-the-secrets) but never creates them.

## Human access: the admission policy

Findings carry five custom RBAC verbs: `approve`, `retry`, `expedite`, `suspend` and `resume`. They let a grant say
"this developer may approve findings, and nothing else". The [status page](../operate/status-page.md) enforces them by
itself, because it writes as its ServiceAccount and checks the user's access first. The [CLI](../operate/cli.md) writes
as the user, so the API server authorizes it. RBAC has no notion of a field, so `update` on findings grants access to
the full object.

`admissionPolicy.enabled` (default `true`) renders a `ValidatingAdmissionPolicy`. It binds each human-writable spec
field to its own verb, inside the API server's admission chain. So the rule holds for the CLI, `kubectl edit`,
`kubectl patch`, server-side apply and raw `curl` alike. The same switch renders the policies for Integration
`spec.replay` and `spec.reset`, and for [BackfillRequests](../integrations/sources/github.md#who-may-run-one): running
one at once needs `backfill` on `integrations`, a request with `spec.via: requested` waits for an operator, and only a
`backfill` holder may decide it. The status page's scoped maintainer verbs appear in no policy, because only the status
page honours them.

```yaml
admissionPolicy:
  enabled: true
  # A controller of your own that legitimately writes Finding spec.
  # The chart's own components are exempted automatically, from their
  # resolved ServiceAccount names. So renaming the release or overriding
  # a serviceAccount.name cannot lock a controller out.
  extraExemptSubjects: []
```

Disabling the policy weakens privilege separation. Every custom verb becomes advisory, and anyone with `update` on
findings can change any field, including forging an approval. On a cluster below 1.30 the template fails to render
instead of silently leaving the policy out. A missing policy looks the same as a working one until someone tests it.

## Values

Values are validated against `values.schema.json`. A mistyped or moved key fails the install instead of being silently
ignored. Settings for one controller live under that controller's top-level key. Only settings that several controllers
share stay global.

### Global: images, CRDs

| Key                 | Default                              | Purpose                                                                                                               |
| ------------------- | ------------------------------------ | --------------------------------------------------------------------------------------------------------------------- |
| `image.repository`  | `ghcr.io/bitwise-media-group/patchy` | Repository prefix (registry included); the binary name is appended                                                    |
| `image.tag`         | `""`                                 | Empty = `v<appVersion>`                                                                                               |
| `image.pullPolicy`  | `IfNotPresent`                       |                                                                                                                       |
| `image.pullSecrets` | `[]`                                 |                                                                                                                       |
| `crds.install`      | `true`                               | Render the CRDs as templates (so upgrades track schema changes)                                                       |
| `crds.keep`         | `true`                               | Stamp `helm.sh/resource-policy: keep`, so uninstall never deletes the CRDs, or the FindingRollup statistics with them |
| `commonLabels`      | `{}`                                 | Extra labels on every rendered object                                                                                 |
| `commonAnnotations` | `{}`                                 | Extra annotations on every rendered object, pods included (per-object annotations win)                                |

You can override the image per component, and these overrides win key by key. A `digest` pins the image over any tag.
The two override keys are:

- `<controller>.image`
- `agent.runners.<harness>.image`: the runner image the job controllers stamp into every Job that harness runs
  (`PATCHY_<HARNESS>_AGENT_IMAGE`). Unlike kustomize, pinning a runner's digest here takes one setting instead of two.

### The pipeline switch-on: the `patchy-config` chart

The controllers stay idle until an `Integration` and a `Forge` exist. You install these with the separate
`patchy-config` chart, into the **same namespace** as the patchy release, after it. Each entry in its `integrations` /
`forges` lists renders one CR. Its `values.schema.json` embeds the CRD spec schemas (regenerated by `mise run codegen`).
So a mistyped `spec` field fails the install client-side before the API server sees it. See the
[install page](../getting-started/install.md#switch-the-pipeline-on-the-patchy-config-chart) for a worked example and
`deploy/kustomize/base/crs.example.yaml` for every field. You create the referenced Secrets yourself. You can also
`kubectl apply` the CRs directly instead of using the chart.

### The webhook entry point

A provider has one webhook URL, so you set up exposure at the chart level: `webhook.host` plus one flavour. Both point
at the **integration-controller**. See [Webhook exposure](webhook.md).

| Key                                                   | Default    | Purpose                                                           |
| ----------------------------------------------------- | ---------- | ----------------------------------------------------------------- |
| `webhook.host`                                        | `""`       | The single external hostname (required when a flavour is enabled) |
| `webhook.ingress.{enabled,className,annotations,tls}` | `false`, … | Plain-Ingress flavour                                             |
| `webhook.httpRoute.{enabled,annotations,parentRefs}`  | `false`, … | Gateway API flavour; TLS is the Gateway's concern                 |

### Shared pipeline config

Each controller renders its own ConfigMap, consumed with `envFrom`. It holds the shared `config.*` keys plus the
controller's own `config` block. Every key is the matching `PATCHY_*` variable without the prefix. You can repeat any
shared key under `<controller>.config` to override it for that controller alone.

| Key                            | Default | Purpose                                                                                 |
| ------------------------------ | ------- | --------------------------------------------------------------------------------------- |
| `config.logLevel`              | `warn`  | `debug`, `info`, `warn`, `error`                                                        |
| `config.maxAttempts`           | `2`     | Agent attempts per finding before it fails (both job controllers)                       |
| `config.priorityAgingInterval` | `24h`   | Wait per effective-priority point of aging boost                                        |
| `config.priorityAgingCap`      | `25`    | Maximum aging boost                                                                     |
| `config.extra`                 | `{}`    | Verbatim `PATCHY_*` keys for every controller; `<controller>.config.extra` wins over it |

The listen (`:8080`), health (`:8081`), artifact (`:9790`) and repository-access (`:9792`) addresses are not values. The
chart hardcodes them everywhere they appear (env, container ports, probes, Services, NetworkPolicies).

### Per-controller blocks

`integrationController`, `sourceController`, `contextController`, `investigationController`, `remediationController`,
`evaluationController`, `statusServer` and `egressBroker` share one shape: `image`, `config` (+ `config.extra`),
`resources`, `podAnnotations`, `podLabels`, `nodeSelector`, `tolerations`, `affinity`,
`serviceAccount.{create,name,annotations}`, and `networkPolicy.create`.

The components that call out of the cluster (integration-controller, context-controller and egress-broker) also take
`networkPolicy.extraEgress`. These are verbatim egress rules appended to their policy, for example for an HTTP proxy
port, a generic integration's endpoint or a cloud metadata address.

A `service.{type,port,nodePort,annotations}` block exists on the components that other components call:

- integration (`:8080`, fronted by `webhook.*`; `NodePort` covers the kind/dev flow)
- source (`:9790`, in-cluster only)
- the status server
- the broker
- the evaluation controller

The controller-specific `config` defaults:

| Key                                                          | Default  | Maps to                                             |
| ------------------------------------------------------------ | -------- | --------------------------------------------------- |
| `integrationController.config.accumulationWindow`            | `1h`     | `PATCHY_ACCUMULATION_WINDOW`                        |
| `investigationController.config.findingMinAge`               | `1h`     | `PATCHY_FINDING_MIN_AGE`                            |
| `investigationController.config.maxConcurrentInvestigations` | `3`      | `PATCHY_MAX_CONCURRENT_INVESTIGATIONS`              |
| `investigationController.config.confidenceThreshold`         | `"0.75"` | `PATCHY_CONFIDENCE_THRESHOLD`                       |
| `remediationController.config.maxConcurrentRemediations`     | `1`      | `PATCHY_MAX_CONCURRENT_REMEDIATIONS`                |
| `remediationController.config.findingTTL`                    | `336h`   | `PATCHY_FINDING_TTL` (`"0"` keeps findings forever) |

You can set anything else a binary reads (see the [configuration reference](../configuration/index.md)) through
`config.extra` / `<controller>.config.extra`.

### Agent sandbox

| Key                                      | Default                                             | Purpose                                                                                                                                                                                                                          |
| ---------------------------------------- | --------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `agent.namespace`                        | `patchy-agents`                                     | Created by the chart with the `restricted` PSS labels                                                                                                                                                                            |
| `agent.createNamespace`                  | `true`                                              | Set `false` when the namespace is managed elsewhere                                                                                                                                                                              |
| `agent.serviceAccount`                   | `patchy-agent`                                      | The Job identity: no Role, token not mounted                                                                                                                                                                                     |
| `agent.jobDeadline` / `agent.jobTTL`     | `1h` / `1h`                                         | `activeDeadlineSeconds` / `ttlSecondsAfterFinished`                                                                                                                                                                              |
| `agent.modelAllowlist`                   | `anthropic/claude-sonnet,anthropic/claude-opus`     | Canonical model ids or family aliases the investigation may request for remediation                                                                                                                                              |
| `agent.investigate.*`                    | `anthropic/claude-sonnet` / `15m` / `25` / `150000` | model/timeout/maxTurns/tokenBudget. These limits are **absolute**: the stage runs on these values. The harness is derived from the model.                                                                                        |
| `agent.remediate.*`                      | `anthropic/claude-sonnet` / `45m`                   | `model`/`timeout`, plus `auto.{maxTurns,tokenBudget}` (80 / 400000) and `manual.{…}` (240 / 1200000). See [the remediation budget](models.md#the-remediation-budget).                                                            |
| `agent.runners.<harness>`                | claude enabled; codex disabled                      | Per-harness `enabled`/`image` plus a `provider` block. Both harnesses are brokered, so they take no `secret*`/`hosts`/`dnsPatterns` (these are ignored with a NOTES warning).                                                    |
| `agent.runners.claude.provider`          | `name: anthropic`                                   | Which model API the egress broker fronts: `anthropic`/`bedrock`/`vertex`/`foundry` + `region`/`regionPrefix`/`projectID`/`resource`/`modelMap`/`env`                                                                             |
| `agent.runners.codex.provider`           | `name: openai`                                      | Codex's broker route: `openai`/`azure` + `resource` (azure)/`modelMap` (required for azure: deployment names)/`env`                                                                                                              |
| `egressBroker.*`                         | deploys when claude or codex is enabled             | The [egress credential broker](../configuration/egress-broker.md): `anthropicSecret`, `anthropicAuth` (`key`/`token`), `foundrySecret`, `openaiSecret`, `azureSecret`, `serviceAccount.annotations`, `networkPolicy.extraEgress` |
| `agent.networkPolicy.create`             | `true`                                              | Default-deny both directions + DNS + artifact + broker + TCP-443-only egress                                                                                                                                                     |
| `agent.networkPolicy.clusterCIDRs`       | RFC-1918 + link-local                               | Cluster-internal ranges excluded from agent egress                                                                                                                                                                               |
| `agent.networkPolicy.clusterDNSPatterns` | `*.svc.cluster.local`                               | Cilium only: cluster-local names every runner may resolve (for the artifact fetch)                                                                                                                                               |
| `agent.networkPolicy.mode`               | `auto`                                              | Hostname-egress dialect: `auto`/`none`/`cilium`/`gke`/`istio` (one policy per runner)                                                                                                                                            |
| `agent.networkPolicy.broadEgress`        | `auto`                                              | Keep the base "443 to anywhere" rule: `auto`/`always`/`never`. See the note below.                                                                                                                                               |
| `agent.networkPolicy.cilium.enabled`     | `false`                                             | Deprecated alias for `mode: cilium`, honoured only while `mode` is `auto`                                                                                                                                                        |
| `agent.networkPolicy.istio.enabled`      | `false`                                             | Deprecated alias for `mode: istio`, honoured only while `mode` is `auto`                                                                                                                                                         |

`mode: auto` reads the cluster's API surface on every render against a live cluster, including every helm-controller
reconcile. It then picks one of:

- `gke`: GKE's `FQDNNetworkPolicy`. It needs a cluster with `--enable-fqdn-network-policy`.
- `cilium`: a real Cilium. It is never selected on GKE, where a `CiliumNetworkPolicy` enforces nothing.
- `none`.

It never selects `istio`. An off-cluster `helm template` sees no cluster and renders `none`, so pin `mode` when you need
a deterministic render. Enabling both Cilium and Istio through the deprecated aliases fails the render, so pick one.

Network policies are **additive**. An FQDN allowlist next to the base policy's "443 to anywhere" rule therefore
restricts nothing. For this reason, `broadEgress: auto` drops that rule whenever a hostname mode is enforcing. See the
[isolation model](../concepts/isolation.md#network-egress-the-floor-and-the-fence) for what each layer requires and what
it doesn't cover.

### The status page and evaluation API

| Key                                                       | Default | Purpose                                                                                |
| --------------------------------------------------------- | ------- | -------------------------------------------------------------------------------------- |
| `statusServer.enabled`                                    | `true`  | Render the [status page](../operate/status-page.md) and everything it needs            |
| `statusServer.auth.{existingSecret,config}`               | none    | The authentication config; with neither set the server serves nothing                  |
| `statusServer.liveTranscripts`                            | `true`  | Stream running agents' conversations (grants pod and log reads in the agent namespace) |
| `statusServer.host`, `.ingress.*`, `.httpRoute.*`         | off     | Expose the page on its own hostname, separate from `webhook.host`                      |
| `statusServer.rbac.userRoles`                             | `false` | Render the example viewer, approver, operator, admin and maintainer ClusterRoles       |
| `evaluationController.enabled`                            | `false` | Run the optional [evaluation-controller](../configuration/evaluation-controller.md)    |
| `evaluationController.auth.{existingSecret,config}`       | none    | The API's OIDC config; **required** when enabled                                       |
| `evaluationController.runners.{claude,codex}`             | claude  | Which evolve-runner images evaluation units run on                                     |
| `evaluationController.workspaceRetention`                 | `168h`  | How long unused workspace bundles are kept                                             |
| `evaluationController.host`, `.ingress.*`, `.httpRoute.*` | off     | Expose the API for evolve clients                                                      |

### Model providers

All claude and codex model traffic goes through the [egress broker](../configuration/egress-broker.md), which deploys
whenever a claude or codex runner is enabled. `agent.runners.claude.provider` and `agent.runners.codex.provider` pick
the API it fronts, and the `egressBroker.*` Secret values hold the credentials. [Models and credentials](models.md) has
the values for every provider, with examples, and the migration from releases before the broker.

## Operational notes

!!! warning "Singletons only"

    Every Deployment the chart renders is `replicas: 1`. The controllers use `strategy: Recreate`, and their
    leader-election Lease protects against a botched rollout. It is not a scaling mechanism. The status server and
    the egress broker hold no state, so they use `RollingUpdate` and stay up during an upgrade. Do not scale the
    Deployments.

- `helm uninstall` deletes the agent namespace, which kills any running agent Job. With `crds.keep` it never deletes the
  CRDs, so the Findings and the all-time FindingRollup statistics survive a reinstall.
- Lint and render locally with `mise run helm-lint`.
- Chart and images carry build-provenance attestations:
  `gh attestation verify --owner bitwise-media-group oci://ghcr.io/bitwise-media-group/patchy/charts/patchy:X.Y.Z`.
