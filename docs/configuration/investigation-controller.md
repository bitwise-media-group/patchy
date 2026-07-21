# investigation-controller

Two engines in one binary. The **gate** admits findings that are `Enhanced`, have a closed accumulation window, and are
older than `--finding-min-age`; admission materializes the `Repository` (if absent) and one immutable `Investigation`
per attempt — the child create is the lease. The **analysis scheduler** then runs agent Jobs under bounded concurrency
in severity order, parses the `PATCHY-EVENT:` stream, stamps the results, and routes the verdict onto the Finding.

```sh
investigation-controller serve --namespace patchy \
  --agent-image ghcr.io/bitwise-media-group/patchy/agent-runner:v0.3.3
```

## Pipeline flags

The [shared flags](index.md#shared-flags-all-five-controllers), plus:

<div class="nowrap-first" markdown>

| Flag                              | Env                                    | Default | Purpose                                             |
| --------------------------------- | -------------------------------------- | ------- | --------------------------------------------------- |
| `--finding-min-age`               | `PATCHY_FINDING_MIN_AGE`               | `1h`    | How old a finding must be before the gate admits it |
| `--max-attempts`                  | `PATCHY_MAX_ATTEMPTS`                  | `2`     | Analysis attempts per finding before it fails       |
| `--max-concurrent-investigations` | `PATCHY_MAX_CONCURRENT_INVESTIGATIONS` | `3`     | Simultaneously running investigation Jobs           |
| `--confidence-threshold`          | `PATCHY_CONFIDENCE_THRESHOLD`          | `0.75`  | Confidence required to queue automated remediation  |
| `--priority-aging-interval`       | `PATCHY_PRIORITY_AGING_INTERVAL`       | `24h`   | Wait per effective-priority point of aging boost    |
| `--priority-aging-cap`            | `PATCHY_PRIORITY_AGING_CAP`            | `25`    | Maximum aging boost                                 |

</div>

## Agent Job flags

Shared with the [remediation-controller](remediation-controller.md) — both job controllers build Jobs the same way:

<div class="nowrap-first" markdown>

| Flag                      | Env                            | Default             | Purpose                                                                                                 |
| ------------------------- | ------------------------------ | ------------------- | ------------------------------------------------------------------------------------------------------- |
| `--agent-image`           | `PATCHY_AGENT_IMAGE`           | —                   | agent-runner container image. **Required**                                                              |
| `--agent-namespace`       | `PATCHY_AGENT_NAMESPACE`       | `patchy-agents`     | Namespace the agent Jobs run in                                                                         |
| `--agent-service-account` | `PATCHY_AGENT_SERVICE_ACCOUNT` | `patchy-agent`      | ServiceAccount for the agent pods                                                                       |
| `--anthropic-secret`      | `PATCHY_ANTHROPIC_SECRET`      | `patchy-anthropic`  | Secret (agent namespace) holding the model credential                                                   |
| `--anthropic-secret-key`  | `PATCHY_ANTHROPIC_SECRET_KEY`  | `api-key`           | Key within that Secret                                                                                  |
| `--anthropic-secret-env`  | `PATCHY_ANTHROPIC_SECRET_ENV`  | `ANTHROPIC_API_KEY` | Env var the credential is injected as; `CLAUDE_CODE_OAUTH_TOKEN` for a `claude setup-token` OAuth token |
| `--job-deadline`          | `PATCHY_JOB_DEADLINE`          | `1h`                | `activeDeadlineSeconds` for an agent Job                                                                |
| `--job-ttl`               | `PATCHY_JOB_TTL`               | `1h`                | `ttlSecondsAfterFinished` for a finished Job                                                            |
| `--model-allowlist`       | `PATCHY_MODEL_ALLOWLIST`       | `claude-sonnet-5`   | Models the investigation may request for remediation (comma-separated)                                  |

</div>

## Stage flags

The investigation stage's limits are **absolute** — the stage runs on exactly these. The two `remediate` ceilings exist
here because this controller clamps what the investigation report requests before it ever reaches the queue.

<div class="nowrap-first" markdown>

| Flag                         | Env                               | Default           | Purpose                                             |
| ---------------------------- | --------------------------------- | ----------------- | --------------------------------------------------- |
| `--investigate-harness`      | `PATCHY_INVESTIGATE_HARNESS`      | `claude`          | Harness the analysis stage runs on                  |
| `--investigate-model`        | `PATCHY_INVESTIGATE_MODEL`        | `claude-sonnet-5` | Model the analysis stage runs on                    |
| `--investigate-timeout`      | `PATCHY_INVESTIGATE_TIMEOUT`      | `15m`             | Wall-clock limit for the analysis stage             |
| `--investigate-max-turns`    | `PATCHY_INVESTIGATE_MAX_TURNS`    | `25`              | Agent turns allowed for the analysis stage          |
| `--investigate-token-budget` | `PATCHY_INVESTIGATE_TOKEN_BUDGET` | `150000`          | Output-token budget for the analysis stage          |
| `--remediate-max-turns`      | `PATCHY_REMEDIATE_MAX_TURNS`      | `80`              | Ceiling on the report's suggested remediation turns |
| `--remediate-token-budget`   | `PATCHY_REMEDIATE_TOKEN_BUDGET`   | `400000`          | Ceiling on the suggested remediation budget         |

</div>

The stage flags are re-serialized into `PATCHY_*` environment variables injected into every investigation pod
([agent-runner](agent-runner.md) reads them), so this controller's flags are the single operator-facing configuration
point for the analysis stage.

## Verdict routing

When the analysis Job completes, the controller stamps the summary onto `Finding.status.investigation`, sets the
scheduling priority, and moves the phase:

| Report says                                                 | Finding moves to                                        |
| ----------------------------------------------------------- | ------------------------------------------------------- |
| `ignore` (false positive)                                   | `Dismissed` — alerts dismissed, issue closed            |
| `manual`                                                    | `HandedOff` — owners take over; `/approve` can revive   |
| `remediate`, confidence < threshold or breaking-change hold | `AwaitingApproval`                                      |
| `remediate`, confidence ≥ threshold                         | `Queued`                                                |
| Stage outcome not `ok`                                      | `Enhanced` (retry) while attempts remain, then `Failed` |

A partial report is never trusted: outcomes other than `ok` (`runtime_error`, `timeout`, `budget_exceeded`,
`report_missing`, `report_invalid`) always retry or fail — they never route on whatever frontmatter survived.
