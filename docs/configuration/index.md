# Configuration

The three controllers share one configuration system; the agent-runner is deliberately different. There are no config
files — configuration is flags and environment only.

## Flags, environment, precedence

Every controller flag `--foo-bar` is bound to the environment variable `PATCHY_FOO_BAR` (dashes become underscores,
uppercased, `PATCHY_` prefix). Precedence, highest first:

1. An explicit flag on the command line
2. The `PATCHY_*` environment variable
3. The built-in default

The Helm chart renders a ConfigMap of `PATCHY_*` variables per controller — the shared `config.*` values plus that
controller's `<controller>.config` block — consumed by its Deployment via `envFrom` — in Kubernetes you configure patchy
with values, not flags.

The **agent-runner** has no flags at all: it reads only `PATCHY_*` environment variables, injected into the Job pod by
the remediation-controller. See [agent-runner](agent-runner.md).

## Shared flags (all three controllers)

Registered as persistent flags on every controller's `serve` command:

<div class="nowrap-first" markdown>

| Flag                            | Env                                  | Default | Purpose                                               |
| ------------------------------- | ------------------------------------ | ------- | ----------------------------------------------------- |
| `--listen-addr`                 | `PATCHY_LISTEN_ADDR`                 | `:8080` | Webhook + health HTTP listen address                  |
| `--webhook-secret-file`         | `PATCHY_WEBHOOK_SECRET_FILE`         | —       | File containing the webhook HMAC secret. **Required** |
| `--github-app-id`               | `PATCHY_GITHUB_APP_ID`               | `0`     | GitHub App ID (App auth)                              |
| `--github-app-private-key-file` | `PATCHY_GITHUB_APP_PRIVATE_KEY_FILE` | —       | PEM file with the App private key                     |
| `--github-token`                | `PATCHY_GITHUB_TOKEN`                | —       | PAT dev fallback; **wins over App auth** if set       |
| `--github-base-url`             | `PATCHY_GITHUB_BASE_URL`             | —       | GitHub API base URL (GHES); empty = api.github.com    |
| `--reconcile-interval`          | `PATCHY_RECONCILE_INTERVAL`          | `1m`    | Reconcile loop interval                               |
| `--log-level`                   | `PATCHY_LOG_LEVEL`                   | `warn`  | Log level: `debug`, `info`, `warn`, or `error`        |

</div>

Every binary also answers `--version` and `--help`.

## GitHub authentication

Two modes, resolved at startup:

- **App auth (production)** — set both `--github-app-id` and `--github-app-private-key-file`. The controllers mint
  short-lived installation tokens scoped to a single repository per operation (read-only for the sandbox clone, write
  for the branch push), resolving each repository's installation automatically.
- **Token auth (development)** — `--github-token` with a personal access token. If set, it wins over App auth.

If neither is configured, startup fails with
`github auth: set --github-token, or --github-app-id with --github-app-private-key-file`.

The webhook secret is always required: `Options.WebhookSecret` errors without `--webhook-secret-file`. The file's
contents (trailing newline trimmed) HMAC-validate every inbound delivery's `X-Hub-Signature-256`, constant-time;
mismatches are rejected with `401`.

## The HTTP surface

Every controller serves three routes on `--listen-addr`:

| Route           | Purpose                                                                                     |
| --------------- | ------------------------------------------------------------------------------------------- |
| `POST /webhook` | GitHub deliveries — `202` accepted, `204` for `ping`, `401` bad signature, `503` queue full |
| `GET /healthz`  | Liveness — always `200`                                                                     |
| `GET /readyz`   | Readiness                                                                                   |

The webhook server runs 4 workers over a queue of 64 (a full queue returns `503` so GitHub redelivers), deduplicates the
last 1024 delivery IDs, and caps request bodies at 25 MiB to match GitHub.

## Telemetry

`PATCHY_TELEMETRY_DIR` (environment-only, no flag) switches OpenTelemetry export to per-signal JSON files; otherwise
standard `OTEL_*` variables select exporters. See [Observability](../observability.md).

## Per-binary reference

- [source-controller](source-controller.md) — the accumulation window
- [context-controller](context-controller.md) — enhancer chain and grace period
- [remediation-controller](remediation-controller.md) — Job orchestration, stage budgets, thresholds
- [agent-runner](agent-runner.md) — the in-pod environment contract
