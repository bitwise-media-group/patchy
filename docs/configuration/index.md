# Configuration

The controllers, the [status-server](status-server.md) and the [egress-broker](egress-broker.md) share one configuration
system; the agent-runner is deliberately different. There is no file of flag settings: configuration is flags and
environment only. A few flags point at a mounted file with its own format (the status server's and evaluation
controller's `--auth-config`, the context-controller's `--static-context-file`), and the forge and provider credentials
are **not** configuration: they live in Secrets referenced by your `Integration` and `Forge` custom resources
([secrets and CRs](../getting-started/install.md#create-the-secrets)), read on demand through the Kubernetes API.

## Flags, environment, precedence

Every controller flag `--foo-bar` is bound to the environment variable `PATCHY_FOO_BAR` (dashes become underscores,
uppercased, `PATCHY_` prefix — so `--claude-agent-image` is `PATCHY_CLAUDE_AGENT_IMAGE`). Precedence, highest first:

1. An explicit flag on the command line
2. The `PATCHY_*` environment variable
3. The built-in default

In Kubernetes you configure patchy with environment, not flags: the Deployments pass nothing but the `serve` subcommand,
and the `PATCHY_*` variables arrive via `envFrom` — one shared ConfigMap in the
[kustomize base](../deployment/kustomize.md), one ConfigMap per controller in the [Helm chart](../deployment/helm.md). A
key a given binary does not bind is simply ignored by that binary, which is why one ConfigMap can serve every binary.

The **agent-runner** has no flags at all: it reads only `PATCHY_*` environment variables, injected into the Job pod by
the job controllers. See [agent-runner](agent-runner.md).

## Shared flags

From `internal/cli.Options` (persistent flags) plus the flags every `serve` command registers. The egress-broker takes
all of them except `--namespace`, since it watches nothing.

| Flag            | Env                  | Default          | Purpose                                                                        |
| --------------- | -------------------- | ---------------- | ------------------------------------------------------------------------------ |
| `--listen-addr` | `PATCHY_LISTEN_ADDR` | `:8080`          | HTTP listen address of the binary's own server, where it has one (see below)   |
| `--log-level`   | `PATCHY_LOG_LEVEL`   | `warn`           | Log level: `debug`, `info`, `warn`, or `error`                                 |
| `--namespace`   | `PATCHY_NAMESPACE`   | `$POD_NAMESPACE` | Namespace the patchy resources live in. **Required** (flag or `POD_NAMESPACE`) |
| `--kubeconfig`  | `PATCHY_KUBECONFIG`  | —                | Kubeconfig path; empty = in-cluster config                                     |
| `--health-addr` | `PATCHY_HEALTH_ADDR` | `:8081`          | `healthz`/`readyz` probe listen address                                        |

Every binary also answers `--version` and `--help`. Every controller runs leader election (a coordination Lease named
per controller, in `--namespace`): insurance against a botched rollout racing two replicas, not a scaling mechanism. The
status-server and egress-broker reconcile nothing and take no lease. Every Deployment stays single-replica.

## The HTTP surface

Beyond the kubelet probes, these are the endpoints patchy serves:

| Endpoint                                                     | Binary                 | Purpose                                                                                   |
| ------------------------------------------------------------ | ---------------------- | ----------------------------------------------------------------------------------------- |
| `POST /github/webhooks` on `--listen-addr`                   | integration-controller | GitHub deliveries: `202` accepted, `204` ping, `401` bad signature, `503` queue full      |
| `POST /google-cloud/webhooks` on `--listen-addr`             | integration-controller | SCC notifications via Pub/Sub push, OIDC-validated                                        |
| `POST /wiz/webhooks` on `--listen-addr`                      | integration-controller | Wiz automation-rule deliveries, bearer-token-validated                                    |
| `POST /generic/<name>/webhooks` on `--listen-addr`           | integration-controller | Generic integrations' findings, HMAC-validated per name                                   |
| `GET /artifacts/…` on `--artifact-addr`                      | source-controller      | The repository tarballs and workspace bundles agent pods fetch (in-cluster only)          |
| `PUT /internal/blobs/{digest}` on `--artifact-internal-addr` | source-controller      | Workspace-bundle uploads from the evaluation-controller (in-cluster only)                 |
| `/api/…`, `/events` and the SPA on `--listen-addr`           | status-server          | The [status page](../operate/status-page.md); routes in [status-server](status-server.md) |
| `/anthropic`, `/bedrock`, … on `--listen-addr`               | egress-broker          | The model-provider routes agent pods call; see [egress-broker](egress-broker.md)          |
| `/api/v1/…` on `--listen-addr`                               | evaluation-controller  | The evolve-facing evaluation API; see [evaluation-controller](evaluation-controller.md)   |
| `GET /healthz` / `GET /readyz` on `--health-addr`            | every binary           | Liveness and readiness probes                                                             |

## Telemetry

`PATCHY_TELEMETRY_DIR` (environment-only, no flag) switches OpenTelemetry export to per-signal JSON files; otherwise
standard `OTEL_*` variables select exporters. See [Observability](../operate/observability.md).

## Per-binary reference

- [integration-controller](integration-controller.md) — webhook receivers, accumulation window, issue projection
- [source-controller](source-controller.md) — Forge/Repository reconcilers and the artifact server
- [context-controller](context-controller.md) — the enhancer chain
- [investigation-controller](investigation-controller.md) — the gate, analysis Jobs, verdict routing
- [remediation-controller](remediation-controller.md) — queue, priority weights, remediation Jobs, push/PR, TTL
- [evaluation-controller](evaluation-controller.md) — the optional remote-evaluation API and unit scheduler
- [agent-runner](agent-runner.md) — the in-pod environment contract
- [egress-broker](egress-broker.md) — the model-credential proxy
- [status-server](status-server.md) — the status page's flags, authentication and authorization
- [CLI commands](../cli/patchy.md) — the generated `patchy` command reference

Model ids, the remediation budget and provider credentials span several binaries; they are documented once, in
[Models and credentials](../deployment/models.md).
