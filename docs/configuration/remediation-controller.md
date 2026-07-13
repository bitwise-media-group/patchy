# remediation-controller

Picks up enhanced findings, runs the agent in an ephemeral Kubernetes Job, and performs **all** GitHub side effects —
labels, comments, alert dismissals, branch pushes, and pull requests. It is the only controller with Kubernetes API
access, and the only place a GitHub write credential exists.

```sh
remediation-controller serve --webhook-secret-file /etc/patchy/webhook/secret \
  --github-app-id 123456 --github-app-private-key-file /etc/patchy/github-app/private-key.pem \
  --agent-image ghcr.io/bitwise-media-group/patchy/agent-runner:v0.1.0
```

## Orchestration flags

All [shared flags](index.md#shared-flags-all-three-controllers), plus:

<div class="nowrap-first" markdown>

| Flag                      | Env                            | Default            | Purpose                                                             |
| ------------------------- | ------------------------------ | ------------------ | ------------------------------------------------------------------- |
| `--agent-image`           | `PATCHY_AGENT_IMAGE`           | —                  | agent-runner container image. **Required**                          |
| `--issue-min-age`         | `PATCHY_ISSUE_MIN_AGE`         | `1h`               | How old a finding must be before pickup                             |
| `--max-attempts`          | `PATCHY_MAX_ATTEMPTS`          | `2`                | Agent-Job attempts per finding before handing to a human            |
| `--confidence-threshold`  | `PATCHY_CONFIDENCE_THRESHOLD`  | `0.75`             | Classification confidence required for automated remediation        |
| `--agent-namespace`       | `PATCHY_AGENT_NAMESPACE`       | `patchy-agents`    | Namespace the agent Jobs run in                                     |
| `--agent-service-account` | `PATCHY_AGENT_SERVICE_ACCOUNT` | `patchy-agent`     | ServiceAccount for the agent pods                                   |
| `--anthropic-secret`      | `PATCHY_ANTHROPIC_SECRET`      | `patchy-anthropic` | Secret (agent namespace) holding the model API key                  |
| `--anthropic-secret-key`  | `PATCHY_ANTHROPIC_SECRET_KEY`  | `api-key`          | Key within that Secret                                              |
| `--job-deadline`          | `PATCHY_JOB_DEADLINE`          | `1h`               | `activeDeadlineSeconds` for an agent Job                            |
| `--job-ttl`               | `PATCHY_JOB_TTL`               | `1h`               | `ttlSecondsAfterFinished` for a finished Job                        |
| `--model-allowlist`       | `PATCHY_MODEL_ALLOWLIST`       | `claude-sonnet-5`  | Models the classifier may request for remediation (comma-separated) |
| `--kubeconfig`            | `PATCHY_KUBECONFIG`            | —                  | Kubeconfig path; empty = in-cluster config                          |

</div>

## Stage flags

The two agent stages carry separate harness, model, and budget knobs. Classification's numbers are absolute limits;
remediation's are **ceilings** — the classifier requests a model, turn count and token budget for the fix, and the
runtime clamps those requests to these values.

<div class="nowrap-first" markdown>

| Flag                       | Env                             | Default           | Purpose                                         |
| -------------------------- | ------------------------------- | ----------------- | ----------------------------------------------- |
| `--classify-harness`       | `PATCHY_CLASSIFY_HARNESS`       | `claude`          | Harness for the classification stage            |
| `--classify-model`         | `PATCHY_CLASSIFY_MODEL`         | `claude-sonnet-5` | Model for the classification stage              |
| `--classify-timeout`       | `PATCHY_CLASSIFY_TIMEOUT`       | `15m`             | Wall-clock limit                                |
| `--classify-max-turns`     | `PATCHY_CLASSIFY_MAX_TURNS`     | `25`              | Agent turns allowed                             |
| `--classify-token-budget`  | `PATCHY_CLASSIFY_TOKEN_BUDGET`  | `150000`          | Output-token budget                             |
| `--remediate-harness`      | `PATCHY_REMEDIATE_HARNESS`      | `claude`          | Harness for the remediation stage               |
| `--remediate-model`        | `PATCHY_REMEDIATE_MODEL`        | `claude-sonnet-5` | Default model when the classifier requests none |
| `--remediate-timeout`      | `PATCHY_REMEDIATE_TIMEOUT`      | `45m`             | Wall-clock limit                                |
| `--remediate-max-turns`    | `PATCHY_REMEDIATE_MAX_TURNS`    | `80`              | Ceiling on requested turns                      |
| `--remediate-token-budget` | `PATCHY_REMEDIATE_TOKEN_BUDGET` | `400000`          | Ceiling on the requested output-token budget    |

</div>

The stage flags are not consumed by the controller itself: it re-serialises them into `PATCHY_*` environment variables
injected into the agent Job pod, so the controller's flags are the single operator-facing configuration point for the
whole pipeline. Token budgets are enforced live — the runner scans output-token usage off the stream and kills the CLI
when the budget is exhausted.

## What it needs from the cluster

- **RBAC** in the agent namespace only: create/get/list/watch/delete on `batch/jobs`; get/list/watch pods and read
  `pods/log` (the `PATCHY-EVENT:` stream); create/get/delete `secrets` and `configmaps` (the per-Job Secret carrying the
  scoped clone token and issue markdown). The chart's Role matches exactly.
- **Retry accounting** — a recoverable Job failure reverts the lease (`classifying → context-enhanced`) and bumps
  `security-attempts`; when attempts reach `--max-attempts`, the finding lands at `attempted` with the owners assigned.
- **Webhook side** — `issue_comment` deliveries drive `/approve` re-runs; `pull_request` deliveries close the loop on
  merge (`remediated`) or unmerged close (`attempted`).
