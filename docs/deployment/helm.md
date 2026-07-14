# Helm chart

The `patchy` chart (in-repo at `helm/chart`) renders the full stack: three singleton controller Deployments — each with
its own ConfigMap, Service, ServiceAccount, and NetworkPolicy — plus the agent namespace with its RBAC and sandbox
policies. It is published to `oci://ghcr.io/bitwise-media-group/patchy/charts/patchy` on every release; release-please
stamps `version` and `appVersion` 1:1 with the app, and the default image tag is `v<appVersion>` — chart `X.Y.Z` runs
images `vX.Y.Z`.

```sh
helm install patchy oci://ghcr.io/bitwise-media-group/patchy/charts/patchy \
  --version <X.Y.Z> --namespace patchy --create-namespace
```

The chart requires Kubernetes ≥ 1.34 (the oldest line not yet end-of-life) and references (never creates) the
[three Secrets](../getting-started/install.md#create-the-secrets).

## Values

Values are validated against `values.schema.json` — a typo'd or relocated key fails the install instead of being
silently ignored. The layout follows the flux-operator convention: everything scoped to one controller lives under its
top-level key (`sourceController`, `contextController`, `remediationController`); only genuinely shared settings are
global.

### Global: images, GitHub, Anthropic

| Key                    | Default                              | Purpose                                                            |
| ---------------------- | ------------------------------------ | ------------------------------------------------------------------ |
| `image.repository`     | `ghcr.io/bitwise-media-group/patchy` | Repository prefix (registry included); the binary name is appended |
| `image.tag`            | `""`                                 | Empty = `v<appVersion>`                                            |
| `image.pullPolicy`     | `IfNotPresent`                       |                                                                    |
| `image.pullSecrets`    | `[]`                                 |                                                                    |
| `commonLabels`         | `{}`                                 | Extra labels on every rendered object                              |
| `commonAnnotations`    | `{}`                                 | Extra annotations on every rendered object, pods included          |
| `github.appSecret`     | `patchy-github-app`                  | Secret (release ns) with `app-id` + `private-key.pem`              |
| `github.webhookSecret` | `patchy-webhook-secret`              | Secret (release ns) with `secret` (webhook HMAC)                   |
| `github.baseURL`       | `""`                                 | GHES API base URL, e.g. `https://ghes.example.com/api/v3`          |
| `anthropic.secret`     | `patchy-anthropic`                   | Secret (**agent** ns) with the model API key                       |
| `anthropic.secretKey`  | `api-key`                            | Key within it                                                      |

Per-component image overrides win key-by-key, and a `digest` pins over any tag: `<controller>.image` and `agent.image` —
the latter is the agent-runner image the remediation-controller stamps into every agent Job (`PATCHY_AGENT_IMAGE`).

### The webhook entry point

A GitHub App has one webhook URL, so exposure is chart-level, not per-controller: the webhook-controller validates each
delivery and routes it to the controllers that consume it — see [Webhook exposure](webhook.md).

| Key                                                                          | Default            | Purpose                                                           |
| ---------------------------------------------------------------------------- | ------------------ | ----------------------------------------------------------------- |
| `webhook.host`                                                               | `""`               | The single external hostname (required when a flavour is enabled) |
| `webhook.ingress.{enabled,className,annotations,tls}`                        | `false`, …         | Plain-Ingress flavour, scoped to `/webhook`                       |
| `webhook.httpRoute.{enabled,annotations,parentRefs}`                         | `false`, …         | Gateway API flavour; TLS is the Gateway's concern                 |
| `webhookController.replicas`                                                 | `2`                | Stateless, unlike the controllers — scale freely                  |
| `webhookController.config.forwardTimeout`                                    | `10s`              | Per-target forward timeout                                        |
| `webhookController.{image,resources,serviceAccount,service,networkPolicy,…}` | as the controllers | Same shape as a controller block                                  |

### Per-controller blocks

Each of `sourceController`, `contextController`, and `remediationController` has the same shape:

| Key                                                                          | Default                                            | Purpose                               |
| ---------------------------------------------------------------------------- | -------------------------------------------------- | ------------------------------------- |
| `image`                                                                      | `{}`                                               | Key-by-key override; `digest` pins    |
| `config`                                                                     | see below                                          | The `PATCHY_*` keys this binary binds |
| `resources`                                                                  | src/ctx 50m/96Mi–500m/256Mi; rem 100m/256Mi–1/1Gi  |                                       |
| `serviceAccount.{create,name,annotations}`                                   | `true` / `""` (= `<fullname>-<controller>`) / `{}` |                                       |
| `service.{type,port,nodePort,annotations}`                                   | `ClusterIP` / `8080` / `null` / `{}`               | `nodePort` for the kind/dev flow      |
| `networkPolicy.create`                                                       | `true`                                             | Webhook + probes in, DNS + TLS out    |
| `podAnnotations` / `podLabels` / `nodeSelector` / `tolerations` / `affinity` | `{}`/`[]`                                          | Per-controller scheduling             |

`remediationController` additionally has `tmpSizeLimit` (default `2Gi`), the scratch emptyDir the repository is cloned
into; its NetworkPolicy alone also allows egress to the Kubernetes API server.

### Pipeline configuration

Each controller renders its own ConfigMap: the shared keys from `config.*` plus its own `config` block; each key becomes
the matching `PATCHY_*` variable. Any shared `config` key can be repeated under `<controller>.config` to override it for
that controller alone. Defaults mirror the [flag defaults](../configuration/index.md):

| Key                                                | Default                                                |
| -------------------------------------------------- | ------------------------------------------------------ |
| `config.logLevel`                                  | `warn` (`debug`, `info`, `warn`, `error`)              |
| `config.reconcileInterval`                         | `60s`                                                  |
| `config.extra`                                     | `{}` — shared verbatim `PATCHY_*`                      |
| `sourceController.config.accumulationWindow`       | `1h`                                                   |
| `contextController.config.enhanceGrace`            | `2m`                                                   |
| `remediationController.config.issueMinAge`         | `1h`                                                   |
| `remediationController.config.maxAttempts`         | `2`                                                    |
| `remediationController.config.confidenceThreshold` | `"0.75"`                                               |
| `remediationController.config.jobDeadline`         | `1h`                                                   |
| `remediationController.config.jobTTL`              | `1h`                                                   |
| `remediationController.config.modelAllowlist`      | `claude-sonnet-5,claude-opus-4-8`                      |
| `remediationController.config.classify.*`          | `claude` / `claude-sonnet-5` / `15m` / `25` / `150000` |
| `remediationController.config.remediate.*`         | `claude` / `claude-sonnet-5` / `45m` / `80` / `400000` |
| `<controller>.config.extra`                        | `{}` — wins over `config.extra`                        |

The template derives the file-path and wiring keys itself (`PATCHY_WEBHOOK_SECRET_FILE`,
`PATCHY_GITHUB_APP_PRIVATE_KEY_FILE`, `PATCHY_AGENT_NAMESPACE`, `PATCHY_AGENT_SERVICE_ACCOUNT`,
`PATCHY_ANTHROPIC_SECRET`, `PATCHY_ANTHROPIC_SECRET_KEY`, `PATCHY_AGENT_IMAGE`); the App ID comes from the Secret via
`secretKeyRef`, never the ConfigMap.

### Agent sandbox

| Key                                         | Default                                                                             |
| ------------------------------------------- | ----------------------------------------------------------------------------------- |
| `agent.namespace`                           | `patchy-agents`                                                                     |
| `agent.createNamespace`                     | `true` — chart creates it (with `restricted` PSS labels)                            |
| `agent.serviceAccount`                      | `patchy-agent` — identity the Job pods run as; no Role, no API token                |
| `agent.image`                               | `{}` — the agent-runner image (`PATCHY_AGENT_IMAGE`)                                |
| `agent.networkPolicy.create`                | `true` — default-deny + TCP-443-only egress                                         |
| `agent.networkPolicy.clusterCIDRs`          | RFC-1918 + link-local ranges, excluded from agent egress                            |
| `agent.networkPolicy.hosts.anthropic`       | `api.anthropic.com`                                                                 |
| `agent.networkPolicy.hosts.github`          | `github.com`, `codeload.github.com`, `objects.githubusercontent.com`                |
| `agent.networkPolicy.hosts.dnsPatterns`     | `*.anthropic.com`, `*.github.com`, `github.com`, `*.githubusercontent.com` (Cilium) |
| `agent.networkPolicy.cilium.enabled`        | `false` — CiliumNetworkPolicy FQDN egress                                           |
| `agent.networkPolicy.istio.enabled`         | `false` — Sidecar + ServiceEntry egress                                             |
| `agent.networkPolicy.istio.istiodNamespace` | `istio-system`                                                                      |

Enabling both Cilium and Istio fails the render — pick one. See the [isolation model](isolation.md#network-egress) for
the requirements each carries.

## Operational notes

!!! warning "Singletons by design"

    All three controllers are `replicas: 1` with `strategy: Recreate` and no leader election — the state machine is
    GitHub issue labels. Do not scale the Deployments.

- `helm uninstall` deletes the agent namespace, including any running agent Job.
- Lint and render locally with `mise run helm-lint`.
- Chart and images carry build-provenance attestations:
  `gh attestation verify --owner bitwise-media-group oci://ghcr.io/bitwise-media-group/patchy/charts/patchy:X.Y.Z`.
