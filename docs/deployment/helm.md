# Helm charts

The `patchy` chart (in-repo at `charts/patchy`) renders the full stack: the `patchy.bitwisemedia.uk` CRDs, five
singleton controller Deployments — each with its own ConfigMap, ServiceAccount, and NetworkPolicy — the two Services
(integration `:8080`, source `:9790`), and the agent namespace with its RBAC and sandbox policies. The companion
`patchy-config` chart (`charts/patchy-config`) renders the `Integration`/`Forge` custom resources — a separate chart
because Helm validates every manifest against the API server before applying anything, so the CRs cannot ride in the
same first install as the CRDs they depend on. Both are published to `oci://ghcr.io/bitwise-media-group/patchy/charts/`
on every release; release-please stamps `version` and `appVersion` 1:1 with the app, and the default image tag is
`v<appVersion>` — chart `X.Y.Z` runs images `vX.Y.Z`.

```sh
helm install patchy oci://ghcr.io/bitwise-media-group/patchy/charts/patchy \
  --version <X.Y.Z> --namespace patchy --create-namespace
helm install patchy-config oci://ghcr.io/bitwise-media-group/patchy/charts/patchy-config \
  --version <X.Y.Z> --namespace patchy -f values.yaml
```

The chart requires Kubernetes ≥ 1.34 (the oldest line not yet end-of-life) and references (never creates) the
[two Secrets](../getting-started/install.md#create-the-secrets).

## Human access: the admission policy

Findings carry five custom RBAC verbs — `approve`, `retry`, `expedite`, `suspend`, `resume` — that let a grant say "this
developer may approve findings, and nothing else". The [status page](../status-ui.md) honours them on its own because it
writes as its ServiceAccount and asks first; the [CLI](../cli.md) writes as the user, so the API server authorizes it,
and RBAC has no notion of a field: `update` on findings grants the whole object.

`admissionPolicy.enabled` (default `true`) renders a `ValidatingAdmissionPolicy` that binds each human-writable spec
field to its own verb, inside the API server's admission chain — so it holds for the CLI, `kubectl edit`,
`kubectl patch`, server-side apply and raw `curl` alike.

```yaml
admissionPolicy:
  enabled: true
  # A controller of your own that legitimately writes Finding spec.
  # The chart's own components are exempted automatically, from their
  # resolved ServiceAccount names — so renaming the release or overriding
  # a serviceAccount.name cannot lock a controller out.
  extraExemptSubjects: []
```

Disabling it is a real reduction in privilege separation, not a cosmetic toggle: every custom verb becomes advisory, and
anyone holding `update` on findings can change any field, including forging an approval. The template refuses to render
on a cluster below 1.30 rather than silently omitting itself, because a missing policy looks exactly like a working one
until someone tests it.

## Values

Values are validated against `values.schema.json` — a typo'd or relocated key fails the install instead of being
silently ignored. Everything specific to one controller lives under that controller's top-level key; only genuinely
shared settings stay global.

### Global: images, CRDs

| Key                 | Default                              | Purpose                                                                                                             |
| ------------------- | ------------------------------------ | ------------------------------------------------------------------------------------------------------------------- |
| `image.repository`  | `ghcr.io/bitwise-media-group/patchy` | Repository prefix (registry included); the binary name is appended                                                  |
| `image.tag`         | `""`                                 | Empty = `v<appVersion>`                                                                                             |
| `image.pullPolicy`  | `IfNotPresent`                       |                                                                                                                     |
| `image.pullSecrets` | `[]`                                 |                                                                                                                     |
| `crds.install`      | `true`                               | Render the CRDs as templates (so upgrades track schema changes)                                                     |
| `crds.keep`         | `true`                               | Stamp `helm.sh/resource-policy: keep` — uninstall never deletes the CRDs, or the FindingRollup statistics with them |
| `commonLabels`      | `{}`                                 | Extra labels on every rendered object                                                                               |
| `commonAnnotations` | `{}`                                 | Extra annotations on every rendered object, pods included (per-object annotations win)                              |

Per-component image overrides win key-by-key, and a `digest` pins over any tag: `<controller>.image` and
`agent.runners.<harness>.image` — the latter is the runner image the job controllers stamp into every Job that harness
runs (`PATCHY_<HARNESS>_AGENT_IMAGE`). Unlike kustomize, pinning a runner's digest here is one knob, not two.

### The pipeline switch-on: the `patchy-config` chart

The controllers idle until an `Integration` and a `Forge` exist. They install via the separate `patchy-config` chart —
into the **same namespace** as the patchy release, after it — whose `integrations` / `forges` list entries each render
one CR. Its `values.schema.json` embeds the CRD spec schemas (regenerated by `mise run codegen`), so a typo'd or
mistyped `spec` field fails the install client-side before the API server sees it. See the
[install page](../getting-started/install.md#switch-the-pipeline-on-the-patchy-config-chart) for a worked example and
`deploy/kustomize/base/crs.example.yaml` for the full field walkthrough. The referenced Secrets are yours to create;
`kubectl apply`-ing the CRs directly instead of using the chart works just as well.

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

| Key                                      | Default                                               | Purpose                                                                                |
| ---------------------------------------- | ----------------------------------------------------- | -------------------------------------------------------------------------------------- |
| `agent.namespace`                        | `patchy-agents`                                       | Created by the chart with the `restricted` PSS labels                                  |
| `agent.createNamespace`                  | `true`                                                | Set `false` when the namespace is managed elsewhere                                    |
| `agent.serviceAccount`                   | `patchy-agent`                                        | The Job identity: no Role, token not mounted                                           |
| `agent.jobDeadline` / `agent.jobTTL`     | `1h` / `1h`                                           | `activeDeadlineSeconds` / `ttlSecondsAfterFinished`                                    |
| `agent.modelAllowlist`                   | `anthropic/claude-sonnet-5,anthropic/claude-opus-4-8` | Canonical model ids the investigation may request for remediation                      |
| `agent.investigate.*`                    | `anthropic/claude-sonnet-5` / `15m` / `25` / `150000` | model/timeout/maxTurns/tokenBudget — **absolute** (harness derived from the model)     |
| `agent.remediate.*`                      | `anthropic/claude-sonnet-5` / `45m` / `80` / `400000` | Same shape; maxTurns/tokenBudget are **ceilings** the report's requests are clamped to |
| `agent.runners.<harness>`                | claude enabled, codex disabled                        | Per-harness `enabled`/`image`/`secret`/`secretKey`/`secretEnv`/`hosts`/`dnsPatterns`   |
| `agent.networkPolicy.create`             | `true`                                                | Default-deny both directions + DNS + artifact + TCP-443-only egress                    |
| `agent.networkPolicy.clusterCIDRs`       | RFC-1918 + link-local                                 | Cluster-internal ranges excluded from agent egress                                     |
| `agent.runners.<harness>.hosts`          | claude: `api.anthropic.com`                           | Per-runner egress allowlist — deliberately **no** forge hosts                          |
| `agent.networkPolicy.clusterDNSPatterns` | `*.svc.cluster.local`                                 | Cilium only: cluster-local names every runner may resolve (for the artifact fetch)     |
| `agent.networkPolicy.mode`               | `auto`                                                | Hostname-egress dialect: `auto`/`none`/`cilium`/`gke`/`istio` — one policy per runner  |
| `agent.networkPolicy.broadEgress`        | `auto`                                                | Keep the base "443 to anywhere" rule: `auto`/`always`/`never` — see the warning below  |
| `agent.networkPolicy.cilium.enabled`     | `false`                                               | Deprecated alias for `mode: cilium`, honoured only while `mode` is `auto`              |
| `agent.networkPolicy.istio.enabled`      | `false`                                               | Deprecated alias for `mode: istio`, honoured only while `mode` is `auto`               |

`mode: auto` reads the cluster's API surface on every render against a live cluster — including every helm-controller
reconcile — and picks `gke` (GKE's `FQDNNetworkPolicy`, needs a cluster with `--enable-fqdn-network-policy`), `cilium`
(a real Cilium; never selected on GKE, where a `CiliumNetworkPolicy` enforces nothing) or `none`. It never selects
`istio`. An off-cluster `helm template` sees no cluster and renders `none`, so pin `mode` when you need a deterministic
render. Enabling both Cilium and Istio via the deprecated aliases fails the render — pick one.

Because network policies are **additive**, an FQDN allowlist alongside the base policy's "443 to anywhere" rule
constrains nothing; `broadEgress: auto` therefore drops that rule whenever a hostname mode is enforcing. See the
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
