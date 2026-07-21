# Helm chart

The `patchy` chart (in-repo at `helm/chart`) renders the full stack: the `patchy.bitwisemedia.uk` CRDs, five singleton
controller Deployments — each with its own ConfigMap, ServiceAccount, and NetworkPolicy — the two Services (integration
`:8080`, source `:9790`), the `Integration`/`Forge` resources from values, and the agent namespace with its RBAC and
sandbox policies. It is published to `oci://ghcr.io/bitwise-media-group/patchy/charts/patchy` on every release;
release-please stamps `version` and `appVersion` 1:1 with the app, and the default image tag is `v<appVersion>` — chart
`X.Y.Z` runs images `vX.Y.Z`.

```sh
helm install patchy oci://ghcr.io/bitwise-media-group/patchy/charts/patchy \
  --version <X.Y.Z> --namespace patchy --create-namespace
```

The chart requires Kubernetes ≥ 1.34 (the oldest line not yet end-of-life) and references (never creates) the
[two Secrets](../getting-started/install.md#create-the-secrets).

## Values

Values are validated against `values.schema.json` — a typo'd or relocated key fails the install instead of being
silently ignored. Everything specific to one controller lives under that controller's top-level key; only genuinely
shared settings stay global.

### Global: images, CRDs, Anthropic

| Key                   | Default                              | Purpose                                                                                                             |
| --------------------- | ------------------------------------ | ------------------------------------------------------------------------------------------------------------------- |
| `image.repository`    | `ghcr.io/bitwise-media-group/patchy` | Repository prefix (registry included); the binary name is appended                                                  |
| `image.tag`           | `""`                                 | Empty = `v<appVersion>`                                                                                             |
| `image.pullPolicy`    | `IfNotPresent`                       |                                                                                                                     |
| `image.pullSecrets`   | `[]`                                 |                                                                                                                     |
| `crds.install`        | `true`                               | Render the CRDs as templates (so upgrades track schema changes)                                                     |
| `crds.keep`           | `true`                               | Stamp `helm.sh/resource-policy: keep` — uninstall never deletes the CRDs, or the FindingRollup statistics with them |
| `commonLabels`        | `{}`                                 | Extra labels on every rendered object                                                                               |
| `commonAnnotations`   | `{}`                                 | Extra annotations on every rendered object, pods included (per-object annotations win)                              |
| `anthropic.secret`    | `patchy-anthropic`                   | Secret (**agent** ns) with the model credential                                                                     |
| `anthropic.secretKey` | `api-key`                            | Key within it                                                                                                       |
| `anthropic.secretEnv` | `ANTHROPIC_API_KEY`                  | Env var it is injected as; `CLAUDE_CODE_OAUTH_TOKEN` for a `claude setup-token` OAuth token                         |

Per-component image overrides win key-by-key, and a `digest` pins over any tag: `<controller>.image` and `agent.image` —
the latter is the agent-runner image both job controllers stamp into every Job (`PATCHY_AGENT_IMAGE`). Unlike kustomize,
pinning the agent digest here is one knob, not two.

### The pipeline switch-on: `integrations` / `forges`

The controllers idle until an `Integration` and a `Forge` exist. Each list entry renders one CR in the release
namespace, `spec` verbatim (the CRD schema validates server-side) — see the
[install page](../getting-started/install.md#install-the-chart-switch-the-pipeline-on) for a worked example and
`deploy/kustomize/base/crs.example.yaml` for the full field walkthrough. The referenced Secrets are yours to create.

### The webhook entry point

A provider has one webhook URL, so exposure is chart-level: `webhook.host` plus exactly one flavour, both fronting the
**integration-controller** — see [Webhook exposure](webhook.md).

| Key                                                   | Default    | Purpose                                                           |
| ----------------------------------------------------- | ---------- | ----------------------------------------------------------------- |
| `webhook.host`                                        | `""`       | The single external hostname (required when a flavour is enabled) |
| `webhook.ingress.{enabled,className,annotations,tls}` | `false`, … | Plain-Ingress flavour                                             |
| `webhook.httpRoute.{enabled,annotations,parentRefs}`  | `false`, … | Gateway API flavour; TLS is the Gateway's concern                 |

### Shared pipeline config

Each controller renders its own ConfigMap (consumed with `envFrom`): the shared `config.*` keys plus its own `config`
block; every key is the matching `PATCHY_*` variable minus the prefix. Any shared key can be repeated under
`<controller>.config` to override it for that controller alone.

| Key                            | Default | Purpose                                                                                 |
| ------------------------------ | ------- | --------------------------------------------------------------------------------------- |
| `config.logLevel`              | `warn`  | `debug`, `info`, `warn`, `error`                                                        |
| `config.maxAttempts`           | `2`     | Agent attempts per finding before it fails (both job controllers)                       |
| `config.priorityAgingInterval` | `24h`   | Wait per effective-priority point of aging boost                                        |
| `config.priorityAgingCap`      | `25`    | Maximum aging boost                                                                     |
| `config.extra`                 | `{}`    | Verbatim `PATCHY_*` keys for every controller; `<controller>.config.extra` wins over it |

The listen (`:8080`), health (`:8081`), and artifact (`:9790`) addresses are not values — the chart hardcodes them
everywhere they appear (env, container ports, probes, Services, NetworkPolicies).

### Per-controller blocks

`integrationController`, `sourceController`, `contextController`, `investigationController`, and `remediationController`
all share one shape: `image`, `config` (+ `config.extra`), `resources`, `podAnnotations`, `podLabels`, `nodeSelector`,
`tolerations`, `affinity`, `serviceAccount.{create,name,annotations}`, and `networkPolicy.create`. A
`service.{type,port,nodePort,annotations}` block exists only on the two controllers anything dials — integration
(`:8080`, fronted by `webhook.*`; `NodePort` covers the kind/dev flow) and source (`:9790`, in-cluster only by design).

The controller-specific `config` defaults:

| Key                                                          | Default  | Maps to                                             |
| ------------------------------------------------------------ | -------- | --------------------------------------------------- |
| `integrationController.config.accumulationWindow`            | `1h`     | `PATCHY_ACCUMULATION_WINDOW`                        |
| `investigationController.config.findingMinAge`               | `1h`     | `PATCHY_FINDING_MIN_AGE`                            |
| `investigationController.config.maxConcurrentInvestigations` | `3`      | `PATCHY_MAX_CONCURRENT_INVESTIGATIONS`              |
| `investigationController.config.confidenceThreshold`         | `"0.75"` | `PATCHY_CONFIDENCE_THRESHOLD`                       |
| `remediationController.config.maxConcurrentRemediations`     | `1`      | `PATCHY_MAX_CONCURRENT_REMEDIATIONS`                |
| `remediationController.config.findingTTL`                    | `336h`   | `PATCHY_FINDING_TTL` (`"0"` keeps findings forever) |

Everything else a binary binds (see the [configuration reference](../configuration/index.md)) is reachable through
`config.extra` / `<controller>.config.extra`.

### Agent sandbox

| Key                                     | Default                                                | Purpose                                                                                |
| --------------------------------------- | ------------------------------------------------------ | -------------------------------------------------------------------------------------- |
| `agent.namespace`                       | `patchy-agents`                                        | Created by the chart with the `restricted` PSS labels                                  |
| `agent.createNamespace`                 | `true`                                                 | Set `false` when the namespace is managed elsewhere                                    |
| `agent.serviceAccount`                  | `patchy-agent`                                         | The Job identity: no Role, token not mounted                                           |
| `agent.image`                           | `{}`                                                   | The agent-runner image (`PATCHY_AGENT_IMAGE`); `digest` pins                           |
| `agent.jobDeadline` / `agent.jobTTL`    | `1h` / `1h`                                            | `activeDeadlineSeconds` / `ttlSecondsAfterFinished`                                    |
| `agent.modelAllowlist`                  | `claude-sonnet-5,claude-opus-4-8`                      | Models the investigation may request for remediation                                   |
| `agent.investigate.*`                   | `claude` / `claude-sonnet-5` / `15m` / `25` / `150000` | harness/model/timeout/maxTurns/tokenBudget — **absolute**                              |
| `agent.remediate.*`                     | `claude` / `claude-sonnet-5` / `45m` / `80` / `400000` | Same shape; maxTurns/tokenBudget are **ceilings** the report's requests are clamped to |
| `agent.networkPolicy.create`            | `true`                                                 | Default-deny both directions + DNS + artifact + TCP-443-only egress                    |
| `agent.networkPolicy.clusterCIDRs`      | RFC-1918 + link-local                                  | Cluster-internal ranges excluded from agent egress                                     |
| `agent.networkPolicy.hosts.anthropic`   | `api.anthropic.com`                                    | The hostname allowlist — deliberately **no** forge hosts                               |
| `agent.networkPolicy.hosts.dnsPatterns` | `*.anthropic.com`, `*.svc.cluster.local`               | Cilium only: names the pod may resolve at all                                          |
| `agent.networkPolicy.cilium.enabled`    | `false`                                                | CiliumNetworkPolicy FQDN egress (needs the DNS proxy)                                  |
| `agent.networkPolicy.istio.enabled`     | `false`                                                | Sidecar (REGISTRY_ONLY) + ServiceEntry (needs native sidecars + Istio CNI)             |

Enabling both Cilium and Istio fails the render — pick one. See the
[isolation model](isolation.md#network-egress-the-floor-and-the-fence) for what each layer requires and what it doesn't
cover.

## Operational notes

!!! warning "Singletons by design"

    All five controllers are `replicas: 1` with `strategy: Recreate`; the leader-election Lease is insurance
    against a botched rollout, not a scaling mechanism. Do not scale the Deployments.

- `helm uninstall` deletes the agent namespace (killing any running agent Job) but — with `crds.keep` — never the CRDs,
  so the Findings and the all-time FindingRollup statistics survive a reinstall.
- Lint and render locally with `mise run helm-lint`.
- Chart and images carry build-provenance attestations:
  `gh attestation verify --owner bitwise-media-group oci://ghcr.io/bitwise-media-group/patchy/charts/patchy:X.Y.Z`.
