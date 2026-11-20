# Observability

Every binary logs structured `log/slog` to **stderr** — stdout is reserved, because the agent-runner's `PATCHY-EVENT:`
stream lives there. The controllers, the egress-broker and the status-server also ship OpenTelemetry traces, metrics,
and logs when an exporter is configured; agent-runner and the `patchy` CLI export no telemetry. Telemetry never blocks
startup: if initialisation fails, the binary falls back to stderr-only logging and keeps serving.

The pipeline itself is observable without any of this: the custom resources **are** the state
(`kubectl get patchy -n patchy`, `kubectl describe finding <name>` for conditions and the phase log), and the
`FindingRollup` objects are the durable statistics record. The metrics below are the same numbers, exported.

## Modes

Each server binary resolves its telemetry mode at startup, in this order:

| Mode     | Selected when                    | Behavior                                                                                  |
| -------- | -------------------------------- | ----------------------------------------------------------------------------------------- |
| file     | `PATCHY_TELEMETRY_DIR` is set    | One JSON file per signal (`traces.json`, `metrics.json`, `logs.json`) under the directory |
| env      | Any active `OTEL_*` exporter var | Standard OTel autoexport / OTLP                                                           |
| disabled | Neither                          | Stderr logging only; no providers installed                                               |

`PATCHY_TELEMETRY_DIR` is environment-only (no flag) and always wins — it exists for tests and local debugging, where a
file you can `jq` beats a collector.

## OTLP configuration

Env mode honours the standard OpenTelemetry variables; any of these being set (to a value other than `none`) activates
it:

```text
OTEL_TRACES_EXPORTER            OTEL_EXPORTER_OTLP_ENDPOINT
OTEL_METRICS_EXPORTER           OTEL_EXPORTER_OTLP_TRACES_ENDPOINT
OTEL_LOGS_EXPORTER              OTEL_EXPORTER_OTLP_METRICS_ENDPOINT
                                OTEL_EXPORTER_OTLP_LOGS_ENDPOINT
```

Point the controllers at a collector by adding the variables to the ConfigMaps via `config.extra` (Helm) or a ConfigMap
patch (kustomize):

```yaml
config:
  extra:
    OTEL_EXPORTER_OTLP_ENDPOINT: http://otel-collector.observability:4317
    OTEL_TRACES_EXPORTER: otlp
    OTEL_METRICS_EXPORTER: otlp
    OTEL_LOGS_EXPORTER: otlp
```

Autoexport's own protocol/endpoint variables work as usual. The resource carries the binary name as `service.name` and
the build version; the instrumentation scope root is `github.com/bitwise-media-group/patchy`, and W3C TraceContext +
Baggage propagators are installed. Logs are bridged — every `slog` record fans out to both stderr and the OTel logger.

## Signals worth alerting on

**Ingestion** (integration-controller):

- `patchy.webhook.deliveries` — counter, attributed by `path` (the provider route), `event`, and `result`. The results
  are `accepted`, `handled`, `duplicate`, `ping`, `unauthenticated`, `oversized`, `undecodable`, `missing-event`,
  `queue-full` and `handler-error`. A rising `unauthenticated` rate means a sender's secret and the Integration's
  disagree. `queue-full` means the controller's internal delivery queue was full: the sender got a `503` and the
  delivery is lost unless it is redelivered (for GitHub, by the
  [redelivery sweep](../integrations/sources/github.md#the-failed-delivery-sweep)).
- `patchy.webhook.delivery` — the span wrapping each delivery's handling (`webhook.path`, `webhook.event`,
  `webhook.delivery`).

**Pipeline outcomes** (emitted at rollup time, exactly once per run/finding, aligned with the CR counters — anyone with
an OTLP backend gets the same numbers the `FindingRollup` objects carry):

- `patchy.stage.runs` — agent runs by `stage`, `harness`, `model`, `outcome`, and `repo`. Alert on a rising non-`ok`
  outcome share.
- `patchy.stage.tokens` — token counts by `stage` and `class` (`input`, `output`, `cache_read`, `cache_creation`).
- `patchy.stage.cost` — agent spend in USD, same attributes as `patchy.stage.runs`. The budget alarm.
- `patchy.finding.completed` — findings reaching a terminal phase, by `phase`, `recommendation`, and `repo`. A rising
  `Failed` share is the pipeline-health signal.
- `patchy.finding.deleted` — finding deletions by `reason` (`ttl` or `manual`).

`repo` is the only high-cardinality attribute (~estate size); constrained backends can drop it with a metric view.

**Agent execution** (`internal/runner`): `patchy.agent.exec.duration` (a histogram of one harness CLI invocation's
wall-clock time), `patchy.agent.exec.timeout` (invocations that hit the stage timeout) and the `patchy.agent.exec` span.
They are recorded inside agent-runner, which installs no telemetry providers, so in agent pods they are no-ops today.
Use `patchy.stage.runs` and the run's status on its `Investigation` or `Remediation` instead.

**Probes:** `GET /healthz` / `GET /readyz` on every server binary's `--health-addr` (`:8081`) feed the Deployments'
liveness and readiness probes.

## Verbosity

`--log-level` / `PATCHY_LOG_LEVEL` sets the stderr handler (and the log bridge) level: `debug`, `info`, `warn` (the
default), or `error`. At `debug`, the webhook server's dedup, queue, and signature decisions all log — the first place
to look when deliveries seem to vanish. The dev overlay ships `info`.
