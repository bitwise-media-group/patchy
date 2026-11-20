# remediation-controller

This controller runs:

- queue admission (approvals and revivals)
- the priority scheduler (bounded concurrency, with aging so no finding waits forever)
- remediation agent Jobs
- the changeset push and pull request, which is the only place a forge **write** credential is used
- the rollup/TTL loop, which makes it the only component that deletes expired Findings

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

The four weights combine the investigation's ratings into the 0–100 scheduling priority the queue sorts on. By default
the split is severity 30%, exploitability 30%, likelihood 20% and impact 20%.

## Agent Job flags

This controller takes the same per-harness runner flags as the
[investigation-controller](investigation-controller.md#agent-job-flags):

- `--claude-agent-image`, `--codex-agent-image` and `--fake-agent-image`
- `--harnesses`
- the broker and provider flags for the brokered claude and codex runners: `--broker-url`, `--claude-provider*`,
  `--claude-model-map`, `--codex-provider`, `--codex-model-map` and `--codex-provider-env`
- `--agent-namespace`, `--agent-service-account`, `--job-deadline` and `--job-ttl`

The legacy `--claude-secret{,-key,-env}` and `--codex-secret{,-key,-env}` flags are ignored. If you set them, the
controller logs a migration notice. The controller refuses to start in any of these cases:

- a claude or codex runner is configured without `--broker-url`
- a harness is given a provider it cannot use (claude takes `anthropic`, `bedrock`, `vertex` or `foundry`; codex takes
  `openai` or `azure`)
- a foundry or azure model map misses an allowlisted model of that harness
- an allowlisted model has no enabled harness that can run it

## Stage flags

The investigation report suggests a model for the fix and estimates its turns and output tokens. The estimate never
shrinks a run. Every remediation gets at least the **auto** budget. An estimate above the auto budget holds the finding
for approval, and an approval grants the estimate up to the **manual** budget.
[The remediation budget](../deployment/models.md#the-remediation-budget) explains the model in full.

The model suggestion is clamped. The spawner holds it to the allowlist and resolves the harness that runs it. The
model's provider decides the runner image and the credential route. `--remediate-model` is the fallback when the
report's suggestion is missing or not on the allowlist. Both flags accept family aliases (`anthropic/claude-opus`),
which resolve to concrete ids once at startup. See
[Model ids and aliases](../deployment/models.md#model-ids-and-aliases).

| Flag                              | Env                                    | Default                   | Purpose                                                        |
| --------------------------------- | -------------------------------------- | ------------------------- | -------------------------------------------------------------- |
| `--model-allowlist`               | `PATCHY_MODEL_ALLOWLIST`               | Sonnet + Opus aliases     | Model ids or family aliases remediation may run                |
| `--remediate-model`               | `PATCHY_REMEDIATE_MODEL`               | `anthropic/claude-sonnet` | Model used when the report's suggestion is missing or unusable |
| `--remediate-timeout`             | `PATCHY_REMEDIATE_TIMEOUT`             | `45m`                     | Wall-clock limit for the remediation stage                     |
| `--remediate-auto-max-turns`      | `PATCHY_REMEDIATE_AUTO_MAX_TURNS`      | `80`                      | Turns an unattended fix gets; estimates above need approval    |
| `--remediate-auto-token-budget`   | `PATCHY_REMEDIATE_AUTO_TOKEN_BUDGET`   | `400000`                  | Output tokens an unattended fix gets; same approval line       |
| `--remediate-manual-max-turns`    | `PATCHY_REMEDIATE_MANUAL_MAX_TURNS`    | `240`                     | Most turns one human approval can grant                        |
| `--remediate-manual-token-budget` | `PATCHY_REMEDIATE_MANUAL_TOKEN_BUDGET` | `1200000`                 | Most output tokens one human approval can grant                |

The manual values must be at least the auto values, or the runner refuses to start. Token budgets are enforced live. The
runner watches the harness's streamed usage events and kills the process group once the total output-token count goes
over the budget.

## Archive flags

Optional long-term archival of every run to S3 or GCS. These flags are shared with the
[investigation-controller](investigation-controller.md) and, for the store location, the
[status-server](status-server.md). See [Run archival](../deployment/archival.md) for what is archived, the identities
involved, and bucket setup.

| Flag                     | Env                           | Default | Purpose                                                                                 |
| ------------------------ | ----------------------------- | ------- | --------------------------------------------------------------------------------------- |
| `--archive-provider`     | `PATCHY_ARCHIVE_PROVIDER`     | none    | `s3` or `gcs`; empty disables archival                                                  |
| `--archive-bucket`       | `PATCHY_ARCHIVE_BUCKET`       | none    | The bucket                                                                              |
| `--archive-prefix`       | `PATCHY_ARCHIVE_PREFIX`       | none    | Key prefix every run is written under                                                   |
| `--archive-region`       | `PATCHY_ARCHIVE_REGION`       | SDK     | S3 bucket region                                                                        |
| `--archive-endpoint`     | `PATCHY_ARCHIVE_ENDPOINT`     | none    | Endpoint override for S3-compatible stores                                              |
| `--archive-kms-key`      | `PATCHY_ARCHIVE_KMS_KEY`      | bucket  | KMS key every write is encrypted with                                                   |
| `--archive-required`     | `PATCHY_ARCHIVE_REQUIRED`     | `false` | Fail a run (outcome `archive_failed`, finding `Failed`) whose archive cannot be written |
| `--archive-retry-window` | `PATCHY_ARCHIVE_RETRY_WINDOW` | `45m`   | How long after the Job finishes a failed upload is retried; must be below `--job-ttl`   |

With archival on, a collected run records `stage.archive` and the `Archived` condition instead of writing a transcript
ConfigMap. A remediation is archived before its push, so with `--archive-required` a run that cannot be archived never
reaches the forge. After the push, `push.json` links the archive to the branch, commit and pull request, and the run's
`status.pushedCommit` records the commit.

## Behavior

- **Queue admission**: an accepted `spec.approval` moves a finding `AwaitingApproval → Queued` or `HandedOff → Queued`.
  The approval can come from a `/approve` issue comment, the status page or `patchy approve`. A human retry of a failed
  remediation moves it `Failed → Queued`. See [Human actions](../concepts/lifecycle.md#human-actions).
- **Scheduling**: when a slot frees, the finding with the highest effective priority moves `Queued → Remediating`.
  Waiting findings gain +1 effective priority per `--priority-aging-interval`, up to `--priority-aging-cap`, so
  low-priority work cannot starve. Each grant creates one immutable `Remediation` child and its agent Job.
- **Verification, then push**: the agent's `commit.sh` must run cleanly and leave real commits. The controller then
  replays the changeset through the GitHub Git Data API (blob → tree → commit → ref) onto the `patchy/<finding>` branch,
  using a scoped write token. It uses no git binary and no clone. It then opens the pull request and moves the finding
  to `InReview`. A recoverable failure re-queues the finding (`Remediating → Queued`) up to `--max-attempts`. When the
  attempts run out, the finding is `Failed`. If the agent reports that the finding cannot be fixed safely, the finding
  moves to `HandedOff`.
- **Rollup statistics and the TTL**: when a finding enters a terminal phase, its stage statistics are added exactly once
  (backed by a finalizer) into the per-scope `FindingRollup` objects. Completed findings older than `--finding-ttl` are
  deleted, and the rollups stay as the lasting record.
