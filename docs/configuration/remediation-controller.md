# remediation-controller

Queue admission (approvals and revivals), the priority scheduler (bounded concurrency, aging against starvation),
remediation agent Jobs, the changeset push + pull request — the only place a forge **write** credential is exercised —
and the rollup/TTL loop, which makes it the one deleter of expired Findings.

```sh
remediation-controller serve --namespace patchy \
  --agent-image ghcr.io/bitwise-media-group/patchy/agent-runner:v0.3.3
```

## Pipeline flags

The [shared flags](index.md#shared-flags-all-five-controllers), plus:

<div class="nowrap-first" markdown>

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

</div>

The four weights combine the investigation's ratings into the 0–100 scheduling priority the queue sorts on (severity 30%
/ exploitability 30% / likelihood 20% / impact 20% by default).

## Agent Job flags

The same Job-construction flags as the [investigation-controller](investigation-controller.md#agent-job-flags):
`--agent-image` (**required**), `--agent-namespace`, `--agent-service-account`, `--anthropic-secret`,
`--anthropic-secret-key`, `--anthropic-secret-env`, `--job-deadline`, `--job-ttl`. This binary additionally validates
`--anthropic-secret-env` against the credential env vars the built-in harnesses accept (`ANTHROPIC_API_KEY`,
`CLAUDE_CODE_OAUTH_TOKEN`, …) and refuses to start otherwise.

## Stage flags

The remediation stage's `max-turns` and `token-budget` are **ceilings**: the investigation report requests its own
model, turn count, and token budget for the fix, and both the controller and the runner clamp those requests to these
bounds (and the model to the allowlist). The model here is the fallback when the report's suggestion is missing or not
allowlisted.

<div class="nowrap-first" markdown>

| Flag                       | Env                             | Default           | Purpose                                      |
| -------------------------- | ------------------------------- | ----------------- | -------------------------------------------- |
| `--remediate-harness`      | `PATCHY_REMEDIATE_HARNESS`      | `claude`          | Harness the remediation stage runs on        |
| `--remediate-model`        | `PATCHY_REMEDIATE_MODEL`        | `claude-sonnet-5` | Default model when the report requests none  |
| `--remediate-timeout`      | `PATCHY_REMEDIATE_TIMEOUT`      | `45m`             | Wall-clock limit for the remediation stage   |
| `--remediate-max-turns`    | `PATCHY_REMEDIATE_MAX_TURNS`    | `80`              | Ceiling on requested turns                   |
| `--remediate-token-budget` | `PATCHY_REMEDIATE_TOKEN_BUDGET` | `400000`          | Ceiling on the requested output-token budget |

</div>

Token budgets are enforced live — the runner watches the harness's streamed usage events and kills the process group
when the cumulative output-token count is exceeded; the `claude` CLI has no such flag itself.

## Behavior

- **Queue admission** — `AwaitingApproval → Queued` and `HandedOff → Queued` on an accepted `/approve` (`spec.approval`,
  written by the integration-controller from the tracking comment webhook).
- **Scheduling** — `Queued → Remediating` when a slot frees, highest effective priority first; waiting findings gain +1
  effective priority per `--priority-aging-interval` up to `--priority-aging-cap`, so low-priority work cannot starve.
  Each grant creates one immutable `Remediation` child and its agent Job.
- **Verification, then push** — the agent's `commit.sh` must run cleanly and leave real commits; the controller then
  replays the changeset through the GitHub Git Data API (blob → tree → commit → ref) onto the `patchy/<finding>` branch
  with a scoped write token — no git binary, no clone — opens the pull request, and moves the finding to `InReview`. A
  recoverable failure re-queues (`Remediating → Queued`) within `--max-attempts`; exhaustion is `Failed`.
- **Rollups and the TTL** — on terminal-phase entry the stage statistics are aggregated exactly-once (finalizer-backed)
  into the per-scope `FindingRollup` objects; completed findings older than `--finding-ttl` are deleted, and the rollups
  remain the durable record.
