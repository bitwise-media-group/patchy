# Helm chart

The `patchy` chart (in-repo at `helm/chart`) renders the full stack: three singleton controller Deployments with
Services and ServiceAccounts, the agent namespace with its RBAC, one ConfigMap of `PATCHY_*` configuration, and baseline
network policies. It is published to `oci://ghcr.io/bitwise-media-group/patchy/charts/patchy` on every release;
release-please stamps `version` and `appVersion` 1:1 with the app, and the default image tag is `v<appVersion>` — chart
`X.Y.Z` runs images `vX.Y.Z`.

```sh
helm install patchy oci://ghcr.io/bitwise-media-group/patchy/charts/patchy \
  --version <X.Y.Z> --namespace patchy --create-namespace
```

The chart requires Kubernetes ≥ 1.25 and references (never creates) the
[three Secrets](../getting-started/install.md#create-the-secrets).

## Values

### Images

| Key                | Default                      | Purpose                                        |
| ------------------ | ---------------------------- | ---------------------------------------------- |
| `image.registry`   | `ghcr.io`                    | Registry prefix                                |
| `image.repository` | `bitwise-media-group/patchy` | Repository prefix; the binary name is appended |
| `image.tag`        | `""`                         | Empty = `v<appVersion>`                        |
| `image.pullPolicy` | `IfNotPresent`               |                                                |
| `imagePullSecrets` | `[]`                         |                                                |

Per-component override maps win key-by-key, and a `digest` pins over any tag: `controllers.source.image`,
`controllers.context.image`, `controllers.remediation.image`, and `agentRunner.image` — the last one is the image the
remediation-controller stamps into every agent Job (`PATCHY_AGENT_IMAGE`).

### GitHub and Anthropic

| Key                    | Default                 | Purpose                                                   |
| ---------------------- | ----------------------- | --------------------------------------------------------- |
| `github.appSecret`     | `patchy-github-app`     | Secret (release ns) with `app-id` + `private-key.pem`     |
| `github.webhookSecret` | `patchy-webhook-secret` | Secret (release ns) with `secret` (webhook HMAC)          |
| `github.baseURL`       | `""`                    | GHES API base URL, e.g. `https://ghes.example.com/api/v3` |
| `anthropic.secret`     | `patchy-anthropic`      | Secret (**agent** ns) with the model API key              |
| `anthropic.secretKey`  | `api-key`               | Key within it                                             |

### Agent sandbox

| Key                     | Default         | Purpose                                              |
| ----------------------- | --------------- | ---------------------------------------------------- |
| `agent.namespace`       | `patchy-agents` | Namespace the agent Jobs run in                      |
| `agent.createNamespace` | `true`          | Chart creates it (with `restricted` PSS labels)      |
| `agent.serviceAccount`  | `patchy-agent`  | Identity the Job pods run as — no Role, no API token |

### Pipeline configuration (`config.*`)

Rendered into one ConfigMap consumed by all three Deployments; each key becomes the matching `PATCHY_*` variable.
Defaults mirror the [flag defaults](../configuration/index.md):

| Key                            | Default                                                    |
| ------------------------------ | ---------------------------------------------------------- |
| `config.listenAddr`            | `:8080`                                                    |
| `config.reconcileInterval`     | `60s`                                                      |
| `config.accumulationWindow`    | `1h`                                                       |
| `config.enhanceGrace`          | `2m`                                                       |
| `config.issueMinAge`           | `1h`                                                       |
| `config.maxAttempts`           | `2`                                                        |
| `config.confidenceThreshold`   | `"0.75"`                                                   |
| `config.jobDeadline`           | `1h`                                                       |
| `config.jobTTL`                | `1h`                                                       |
| `config.modelAllowlist`        | `claude-sonnet-5,claude-opus-4-8`                          |
| `config.classify.harness`      | `claude`                                                   |
| `config.classify.model`        | `claude-sonnet-5`                                          |
| `config.classify.timeout`      | `15m`                                                      |
| `config.classify.maxTurns`     | `25`                                                       |
| `config.classify.tokenBudget`  | `150000`                                                   |
| `config.remediate.harness`     | `claude`                                                   |
| `config.remediate.model`       | `claude-sonnet-5`                                          |
| `config.remediate.timeout`     | `45m`                                                      |
| `config.remediate.maxTurns`    | `80`                                                       |
| `config.remediate.tokenBudget` | `400000`                                                   |
| `config.extra`                 | `{}` — verbatim `PATCHY_*` → value; wins over derived keys |

The template derives the file-path and wiring keys itself (`PATCHY_WEBHOOK_SECRET_FILE`,
`PATCHY_GITHUB_APP_PRIVATE_KEY_FILE`, `PATCHY_AGENT_NAMESPACE`, `PATCHY_AGENT_SERVICE_ACCOUNT`,
`PATCHY_ANTHROPIC_SECRET`, `PATCHY_ANTHROPIC_SECRET_KEY`, `PATCHY_AGENT_IMAGE`); the App ID comes from the Secret via
`secretKeyRef`, never the ConfigMap.

### Controllers, Service, scheduling

| Key                                              | Default                                     |
| ------------------------------------------------ | ------------------------------------------- |
| `controllers.source.resources`                   | requests 50m/96Mi · limits 500m/256Mi       |
| `controllers.context.resources`                  | requests 50m/96Mi · limits 500m/256Mi       |
| `controllers.remediation.resources`              | requests 100m/256Mi · limits 1/1Gi          |
| `controllers.remediation.tmpSizeLimit`           | `2Gi` (scratch emptyDir for the repo clone) |
| `service.type`                                   | `ClusterIP`                                 |
| `service.port`                                   | `8080`                                      |
| `service.nodePorts.{source,context,remediation}` | `null` (NodePort type only)                 |
| `commonLabels` / `podAnnotations` / `podLabels`  | `{}`                                        |
| `nodeSelector` / `tolerations` / `affinity`      | `{}` / `[]` / `{}`                          |

### Network policy

| Key                                    | Default                                                                             |
| -------------------------------------- | ----------------------------------------------------------------------------------- |
| `networkPolicy.enabled`                | `true` — baseline L3/L4 policies                                                    |
| `networkPolicy.clusterCIDRs`           | RFC-1918 + link-local ranges, excluded from agent egress                            |
| `networkPolicy.agentHosts.anthropic`   | `api.anthropic.com`                                                                 |
| `networkPolicy.agentHosts.github`      | `github.com`, `codeload.github.com`, `objects.githubusercontent.com`                |
| `networkPolicy.agentHosts.dnsPatterns` | `*.anthropic.com`, `*.github.com`, `github.com`, `*.githubusercontent.com` (Cilium) |
| `networkPolicy.cilium.enabled`         | `false` — CiliumNetworkPolicy FQDN egress                                           |
| `networkPolicy.istio.enabled`          | `false` — Sidecar + ServiceEntry egress                                             |
| `networkPolicy.istio.istiodNamespace`  | `istio-system`                                                                      |

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
