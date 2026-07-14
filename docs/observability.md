# Observability

Every binary logs structured `log/slog` to **stderr** — stdout is reserved, because the agent-runner's `PATCHY-EVENT:`
stream lives there — and ships OpenTelemetry traces, metrics, and logs when an exporter is configured. Telemetry never
blocks startup: if initialisation fails, the binary falls back to stderr-only logging and keeps serving.

## Modes

Each controller resolves its telemetry mode at startup, in this order:

<div class="nowrap-first" markdown>

| Mode     | Selected when                    | Behavior                                                                                  |
| -------- | -------------------------------- | ----------------------------------------------------------------------------------------- |
| file     | `PATCHY_TELEMETRY_DIR` is set    | One JSON file per signal (`traces.json`, `metrics.json`, `logs.json`) under the directory |
| env      | Any active `OTEL_*` exporter var | Standard OTel autoexport / OTLP                                                           |
| disabled | Neither                          | Stderr logging only; no providers installed                                               |

</div>

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

Point the controllers at a collector by adding the variables to the ConfigMaps via `config.extra`:

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

- `patchy.webhook.deliveries` — counter, attributed by event type and result. A rising `error` or dropped-delivery rate
  means GitHub deliveries are failing HMAC validation or the queue is full.
- `patchy.webhook.delivery` — the span wrapping each delivery's handling.
- `GET /healthz` / `GET /readyz` on every controller feed the Deployments' liveness and readiness probes.

## Verbosity

`--log-level` / `PATCHY_LOG_LEVEL` sets the stderr handler (and the log bridge) level: `debug`, `info`, `warn` (the
default), or `error`. At `debug`, the webhook server's dedup, queue, and signature decisions all log — the first place
to look when deliveries seem to vanish.
