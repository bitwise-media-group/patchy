# remediation-controller

Queue admission (approvals and revivals), the priority scheduler (bounded concurrency, aging against starvation),
remediation agent Jobs, the changeset push + pull request — the only place a forge **write** credential is exercised —
and the rollup/TTL loop, which makes it the one deleter of expired Findings.

```sh
remediation-controller serve --namespace patchy \
  --claude-agent-image ghcr.io/bitwise-media-group/patchy/claude-agent-runner:v0.6.0
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

The same per-harness runner flags as the [investigation-controller](investigation-controller.md#agent-job-flags):
`--claude-agent-image` / `--codex-agent-image` / `--fake-agent-image`, `--harnesses`, the per-harness credential triples
(`--claude-secret{,-key,-env}`, `--codex-secret{,-key,-env}`), `--agent-namespace`, `--agent-service-account`,
`--job-deadline`, `--job-ttl`. Each `--<harness>-secret-env` is validated against the credential env vars that harness
accepts (claude: `ANTHROPIC_API_KEY` / `CLAUDE_CODE_OAUTH_TOKEN`; codex: `OPENAI_API_KEY`) and the controller refuses to
start on a mismatch, on a missing credential for an enabled harness, or on an allowlisted model no enabled harness can
run.

## Stage flags

The remediation stage's `max-turns` and `token-budget` are **ceilings**: the investigation report requests its own
model, turn count, and token budget for the fix. This controller's spawner clamps the requested model to the allowlist
and resolves the harness that runs it (the model's provider decides the runner image and credential); the runner clamps
the turns and budget. The `--remediate-model` here is the canonical fallback model when the report's suggestion is
missing or off the allowlist; its harness is derived from it.

<div class="nowrap-first" markdown>

| Flag                       | Env                             | Default                     | Purpose                                         |
| -------------------------- | ------------------------------- | --------------------------- | ----------------------------------------------- |
| `--model-allowlist`        | `PATCHY_MODEL_ALLOWLIST`        | canonical ids               | Canonical model ids remediation may run         |
| `--remediate-model`        | `PATCHY_REMEDIATE_MODEL`        | `anthropic/claude-sonnet-5` | Canonical default when the report requests none |
| `--remediate-timeout`      | `PATCHY_REMEDIATE_TIMEOUT`      | `45m`                       | Wall-clock limit for the remediation stage      |
| `--remediate-max-turns`    | `PATCHY_REMEDIATE_MAX_TURNS`    | `80`                        | Ceiling on requested turns                      |
| `--remediate-token-budget` | `PATCHY_REMEDIATE_TOKEN_BUDGET` | `400000`                    | Ceiling on the requested output-token budget    |

</div>

Token budgets are enforced live — the runner watches the harness's streamed usage events and kills the process group
when the cumulative output-token count is exceeded; the harness CLI has no such flag itself.

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
