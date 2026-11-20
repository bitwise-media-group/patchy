# remediation-controller

Queue admission (approvals and revivals), the priority scheduler (bounded concurrency, aging against starvation),
remediation agent Jobs, the changeset push + pull request — the only place a forge **write** credential is exercised —
and the rollup/TTL loop, which makes it the one deleter of expired Findings.

```sh
remediation-controller serve --namespace patchy \
  --claude-agent-image ghcr.io/bitwise-media-group/patchy/claude-agent-runner:vX.Y.Z # match your installed release
```

## Pipeline flags

The [shared flags](index.md#shared-flags), plus:

| Flag                               | Env                                     | Default          | Purpose                                                                      |
| ---------------------------------- | --------------------------------------- | ---------------- | ---------------------------------------------------------------------------- |
| `--max-attempts`                   | `PATCHY_MAX_ATTEMPTS`                   | `2`              | Remediation attempts per finding before it fails                             |
| `--max-concurrent-remediations`    | `PATCHY_MAX_CONCURRENT_REMEDIATIONS`    | `1`              | Simultaneously running remediation Jobs                                      |
| `--priority-aging-interval`        | `PATCHY_PRIORITY_AGING_INTERVAL`        | `24h`            | Wait per effective-priority point of aging boost                             |
| `--priority-aging-cap`             | `PATCHY_PRIORITY_AGING_CAP`             | `25`             | Maximum aging boost                                                          |
| `--priority-weight-severity`       | `PATCHY_PRIORITY_WEIGHT_SEVERITY`       | `0.3`            | Scheduling-priority weight of the scanner severity                           |
| `--priority-weight-exploitability` | `PATCHY_PRIORITY_WEIGHT_EXPLOITABILITY` | `0.3`            | Weight of the assessed exploitability                                        |
| `--priority-weight-likelihood`     | `PATCHY_PRIORITY_WEIGHT_LIKELIHOOD`     | `0.2`            | Weight of the assessed likelihood                                            |
| `--priority-weight-impact`         | `PATCHY_PRIORITY_WEIGHT_IMPACT`         | `0.2`            | Weight of the assessed impact                                                |
| `--finding-ttl`                    | `PATCHY_FINDING_TTL`                    | `336h` (14 days) | How long completed findings are kept before deletion; `0` keeps them forever |

The four weights combine the investigation's ratings into the 0–100 scheduling priority the queue sorts on (severity 30%
/ exploitability 30% / likelihood 20% / impact 20% by default).

## Agent Job flags

The same per-harness runner flags as the [investigation-controller](investigation-controller.md#agent-job-flags):
`--claude-agent-image` / `--codex-agent-image` / `--fake-agent-image`, `--harnesses`, the broker/provider flags for the
brokered claude and codex runners (`--broker-url`, `--claude-provider*`, `--claude-model-map`, `--codex-provider`,
`--codex-model-map`, `--codex-provider-env`), `--agent-namespace`, `--agent-service-account`, `--job-deadline`,
`--job-ttl`. The legacy `--claude-secret{,-key,-env}` and `--codex-secret{,-key,-env}` are ignored (customizing them
logs a migration notice). The controller refuses to start on a claude or codex runner without `--broker-url`, on a
provider its harness cannot front, on a foundry or azure model map that misses an allowlisted model of that harness, or
on an allowlisted model no enabled harness can run.

## Stage flags

The investigation report suggests a model for the fix and estimates its turns and output tokens. The estimate never
shrinks a run: every remediation gets at least the **auto** budget, an estimate above it holds the finding for approval,
and an approval grants the estimate up to the **manual** budget.
[The remediation budget](../deployment/models.md#the-remediation-budget) explains the model in full.

The model suggestion is clamped: the spawner holds it to the allowlist and resolves the harness that runs it (the
model's provider decides the runner image and credential route). `--remediate-model` is the fallback when the report's
suggestion is missing or off the allowlist. Both flags accept family aliases (`anthropic/claude-opus`), resolved to
concrete ids once at startup; see [Model ids and aliases](../deployment/models.md#model-ids-and-aliases).

| Flag                              | Env                                    | Default                   | Purpose                                                        |
| --------------------------------- | -------------------------------------- | ------------------------- | -------------------------------------------------------------- |
| `--model-allowlist`               | `PATCHY_MODEL_ALLOWLIST`               | Sonnet + Opus aliases     | Model ids or family aliases remediation may run                |
| `--remediate-model`               | `PATCHY_REMEDIATE_MODEL`               | `anthropic/claude-sonnet` | Model used when the report's suggestion is missing or unusable |
| `--remediate-timeout`             | `PATCHY_REMEDIATE_TIMEOUT`             | `45m`                     | Wall-clock limit for the remediation stage                     |
| `--remediate-auto-max-turns`      | `PATCHY_REMEDIATE_AUTO_MAX_TURNS`      | `80`                      | Turns an unattended fix gets; estimates above need approval    |
| `--remediate-auto-token-budget`   | `PATCHY_REMEDIATE_AUTO_TOKEN_BUDGET`   | `400000`                  | Output tokens an unattended fix gets; same approval line       |
| `--remediate-manual-max-turns`    | `PATCHY_REMEDIATE_MANUAL_MAX_TURNS`    | `240`                     | Most turns one human approval can grant                        |
| `--remediate-manual-token-budget` | `PATCHY_REMEDIATE_MANUAL_TOKEN_BUDGET` | `1200000`                 | Most output tokens one human approval can grant                |

The manual values must be at least the auto values; the runner refuses to start otherwise. Token budgets are enforced
live: the runner watches the harness's streamed usage events and kills the process group once the cumulative
output-token count is exceeded.

## Behavior

- **Queue admission** — `AwaitingApproval → Queued` and `HandedOff → Queued` on an accepted `spec.approval`, whether a
  `/approve` issue comment, the status page or `patchy approve` wrote it. `Failed → Queued` on a human retry of a failed
  remediation. See [Human actions](../concepts/lifecycle.md#human-actions).
- **Scheduling** — `Queued → Remediating` when a slot frees, highest effective priority first; waiting findings gain +1
  effective priority per `--priority-aging-interval` up to `--priority-aging-cap`, so low-priority work cannot starve.
  Each grant creates one immutable `Remediation` child and its agent Job.
- **Verification, then push** — the agent's `commit.sh` must run cleanly and leave real commits; the controller then
  replays the changeset through the GitHub Git Data API (blob → tree → commit → ref) onto the `patchy/<finding>` branch
  with a scoped write token — no git binary, no clone — opens the pull request, and moves the finding to `InReview`. A
  recoverable failure re-queues (`Remediating → Queued`) within `--max-attempts`; exhaustion is `Failed`. An agent that
  reports the finding is not safely fixable moves it to `HandedOff`.
- **Rollup statistics and the TTL** — on terminal-phase entry the stage statistics are aggregated exactly-once
  (finalizer-backed) into the per-scope `FindingRollup` objects; completed findings older than `--finding-ttl` are
  deleted, and the rollups remain the durable record.
