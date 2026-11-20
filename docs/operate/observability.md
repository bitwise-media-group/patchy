# Observability

Every binary writes structured `log/slog` logs to **stderr**. Stdout is reserved for the agent-runner's `PATCHY-EVENT:`
stream. The controllers, the egress-broker and the status-server also send OpenTelemetry traces, metrics and logs when
you configure an exporter. The agent-runner and the `patchy` CLI export no telemetry. Telemetry never blocks startup. If
it fails to initialise, the binary falls back to logging on stderr only and keeps serving.

You can observe the pipeline without any of this. The custom resources **are** the state. Use
`kubectl get patchy -n patchy`, or `kubectl describe finding <name>` for conditions and the phase log. The
`FindingRollup` objects are the lasting record of statistics. The metrics below export the same numbers.

## Modes

Each server binary picks its telemetry mode at startup, checking in this order:

| Mode     | Selected when                    | Behavior                                                                                  |
| -------- | -------------------------------- | ----------------------------------------------------------------------------------------- |
| file     | `PATCHY_TELEMETRY_DIR` is set    | One JSON file per signal (`traces.json`, `metrics.json`, `logs.json`) under the directory |
| env      | Any active `OTEL_*` exporter var | Standard OTel autoexport / OTLP                                                           |
| disabled | Neither                          | Stderr logging only; no providers installed                                               |

`PATCHY_TELEMETRY_DIR` is environment-only (no flag) and always takes priority. It exists for tests and local debugging,
where a file you can read with `jq` is easier than a collector.

## OTLP configuration

Env mode uses the standard OpenTelemetry variables. Setting any of these to a value other than `none` turns it on:

```text
OTEL_TRACES_EXPORTER            OTEL_EXPORTER_OTLP_ENDPOINT
OTEL_METRICS_EXPORTER           OTEL_EXPORTER_OTLP_TRACES_ENDPOINT
OTEL_LOGS_EXPORTER              OTEL_EXPORTER_OTLP_METRICS_ENDPOINT
                                OTEL_EXPORTER_OTLP_LOGS_ENDPOINT
```

To point the controllers at a collector, add the variables to the ConfigMaps. Use `config.extra` with Helm, or a
ConfigMap patch with kustomize:

```yaml
config:
  extra:
    OTEL_EXPORTER_OTLP_ENDPOINT: http://otel-collector.observability:4317
    OTEL_TRACES_EXPORTER: otlp
    OTEL_METRICS_EXPORTER: otlp
    OTEL_LOGS_EXPORTER: otlp
```

Autoexport's own protocol and endpoint variables work as usual. Other details:

- The resource carries the binary name as `service.name`, plus the build version.
- The instrumentation scope root is `github.com/bitwise-media-group/patchy`.
- The W3C TraceContext and Baggage propagators are installed.
- Logs are bridged: every `slog` record goes to both stderr and the OTel logger.

## Signals worth alerting on

**Ingestion** (integration-controller):

- `patchy.webhook.deliveries`: a counter with the attributes `path` (the provider route), `event` and `result`. The
  results are `accepted`, `handled`, `duplicate`, `ping`, `unauthenticated`, `oversized`, `undecodable`,
  `missing-event`, `queue-full` and `handler-error`. A rising `unauthenticated` rate means a sender's secret does not
  match the Integration's. `queue-full` means the controller's internal delivery queue was full. The sender got a `503`,
  and the delivery is lost unless it is redelivered (for GitHub, by the
  [redelivery sweep](../integrations/sources/github.md#the-failed-delivery-sweep)).
- `patchy.webhook.delivery`: the span around each delivery's handling (`webhook.path`, `webhook.event`,
  `webhook.delivery`).

**Pipeline outcomes** are emitted at rollup time, once and only once per run or finding. They match the CR counters, so
anyone with an OTLP backend gets the same numbers the `FindingRollup` objects carry.

- `patchy.stage.runs`: agent runs by `stage`, `harness`, `model`, `outcome` and `repo`. Alert when the share of outcomes
  other than `ok` rises.
- `patchy.stage.tokens`: token counts by `stage` and `class` (`input`, `output`, `cache_read`, `cache_creation`).
- `patchy.stage.cost`: agent spend in USD, with the same attributes as `patchy.stage.runs`. Use it as the budget alarm.
- `patchy.finding.completed`: findings that reach a terminal phase, by `phase`, `recommendation` and `repo`. A rising
  `Failed` share is the signal for pipeline health.
- `patchy.finding.deleted`: finding deletions by `reason` (`ttl` or `manual`).

`repo` is the only high-cardinality attribute (it grows with the number of repositories). Backends with limits can drop
it with a metric view.

**Agent execution** (`internal/runner`) has three signals:

- `patchy.agent.exec.duration`: a histogram of the wall-clock time of one harness CLI invocation
- `patchy.agent.exec.timeout`: invocations that hit the stage timeout
- the `patchy.agent.exec` span

These are recorded inside agent-runner, which installs no telemetry providers. So in agent pods they currently do
nothing. Use `patchy.stage.runs` and the run's status on its `Investigation` or `Remediation` instead.

**Probes:** `GET /healthz` / `GET /readyz` on every server binary's `--health-addr` (`:8081`) feed the Deployments'
liveness and readiness probes.

## Verbosity

`--log-level` / `PATCHY_LOG_LEVEL` sets the level of the stderr handler and the log bridge: `debug`, `info`, `warn` (the
default) or `error`. At `debug`, the webhook server logs all its dedup, queue and signature decisions. Look there first
when deliveries seem to disappear. The dev overlay ships `info`.
