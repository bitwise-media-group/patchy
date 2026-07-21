# Configuration

The five controllers share one configuration system; the agent-runner is deliberately different. There are no config
files — configuration is flags and environment only, and the GitHub credentials are **not** configuration: they live in
Secrets referenced by your `Integration` and `Forge` custom resources
([secrets and CRs](../getting-started/install.md#create-the-secrets)), read on demand through the Kubernetes API.

## Flags, environment, precedence

Every controller flag `--foo-bar` is bound to the environment variable `PATCHY_FOO_BAR` (dashes become underscores,
uppercased, `PATCHY_` prefix — so `--agent-image` is `PATCHY_AGENT_IMAGE`). Precedence, highest first:

1. An explicit flag on the command line
2. The `PATCHY_*` environment variable
3. The built-in default

In Kubernetes you configure patchy with environment, not flags: the Deployments pass nothing but the `serve` subcommand,
and the `PATCHY_*` variables arrive via `envFrom` — one shared ConfigMap in the
[kustomize base](../deployment/kustomize.md), one ConfigMap per controller in the [Helm chart](../deployment/helm.md). A
key a given binary does not bind is simply ignored by that binary, which is why one ConfigMap can serve all five.

The **agent-runner** has no flags at all: it reads only `PATCHY_*` environment variables, injected into the Job pod by
the job controllers. See [agent-runner](agent-runner.md).

## Shared flags (all five controllers)

From `internal/cli.Options` (persistent flags) plus the flags every `serve` command registers:

<div class="nowrap-first" markdown>

| Flag            | Env                  | Default          | Purpose                                                                        |
| --------------- | -------------------- | ---------------- | ------------------------------------------------------------------------------ |
| `--listen-addr` | `PATCHY_LISTEN_ADDR` | `:8080`          | HTTP listen address — only the integration-controller's webhook server uses it |
| `--log-level`   | `PATCHY_LOG_LEVEL`   | `warn`           | Log level: `debug`, `info`, `warn`, or `error`                                 |
| `--namespace`   | `PATCHY_NAMESPACE`   | `$POD_NAMESPACE` | Namespace the patchy resources live in. **Required** (flag or `POD_NAMESPACE`) |
| `--kubeconfig`  | `PATCHY_KUBECONFIG`  | —                | Kubeconfig path; empty = in-cluster config                                     |
| `--health-addr` | `PATCHY_HEALTH_ADDR` | `:8081`          | `healthz`/`readyz` probe listen address                                        |

</div>

Every binary also answers `--version` and `--help`. All five controllers run leader election (a coordination Lease named
per controller, in `--namespace`) — insurance against a botched rollout racing two replicas, not a scaling mechanism;
the Deployments stay single-replica.

## The HTTP surface

Only two controllers serve anything beyond kubelet probes:

| Endpoint                                          | Controller             | Purpose                                                                               |
| ------------------------------------------------- | ---------------------- | ------------------------------------------------------------------------------------- |
| `POST /github/webhooks` on `--listen-addr`        | integration-controller | GitHub deliveries — `202` accepted, `204` ping, `401` bad signature, `503` queue full |
| `GET /artifacts/…` on `--artifact-addr`           | source-controller      | The repository tarballs agent pods fetch (in-cluster only)                            |
| `GET /healthz` / `GET /readyz` on `--health-addr` | every controller       | Liveness / readiness probes                                                           |

## Telemetry

`PATCHY_TELEMETRY_DIR` (environment-only, no flag) switches OpenTelemetry export to per-signal JSON files; otherwise
standard `OTEL_*` variables select exporters. See [Observability](../observability.md).

## Per-binary reference

- [integration-controller](integration-controller.md) — webhook receivers, accumulation window, issue projection
- [source-controller](source-controller.md) — Forge/Repository reconcilers and the artifact server
- [context-controller](context-controller.md) — the enhancer chain
- [investigation-controller](investigation-controller.md) — the gate, analysis Jobs, verdict routing
- [remediation-controller](remediation-controller.md) — queue, priority weights, remediation Jobs, push/PR, TTL
- [agent-runner](agent-runner.md) — the in-pod environment contract
