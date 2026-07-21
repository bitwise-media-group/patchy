# agent-runner

The in-pod coding-agent runtime: one stage per Job — `investigate` or `remediate` — via `claude -p`. It never talks to
GitHub or the Kubernetes API, holds no credentials beyond the model key, and has no flags — configuration is exclusively
`PATCHY_*` environment variables, injected into the Job pod by the job controllers. Results leave the pod as a
`PATCHY-EVENT:` JSONL stream on stdout (which is why all patchy logging goes to stderr).

You normally never configure the agent-runner directly: the
[investigation-controller](investigation-controller.md#stage-flags) and
[remediation-controller](remediation-controller.md#stage-flags) stage flags become this environment. The contract below
matters when debugging a Job spec or running the runtime standalone.

## Identity and phase

<div class="nowrap-first" markdown>

| Env                | Default          | Purpose                                                                                           |
| ------------------ | ---------------- | ------------------------------------------------------------------------------------------------- |
| `PATCHY_REPO`      | — (**required**) | `owner/name` of the repository under analysis                                                     |
| `PATCHY_FINDING`   | — (**required**) | Name of the owning Finding resource — echoed in every event, and the branch is `patchy/<finding>` |
| `PATCHY_BASE_SHA`  | —                | The remote commit the workspace tree corresponds to (the changeset's push base)                   |
| `PATCHY_PHASE`     | `investigate`    | `investigate` or `remediate`                                                                      |
| `PATCHY_WORKSPACE` | `/workspace`     | Pod workspace root (`repo/`, `input/`, `reports/`)                                                |

</div>

## Stage configuration

Mirrors of the controllers' stage flags, same defaults: `PATCHY_INVESTIGATE_HARNESS`, `PATCHY_INVESTIGATE_MODEL`,
`PATCHY_INVESTIGATE_TIMEOUT` (`15m`), `PATCHY_INVESTIGATE_MAX_TURNS` (`25`), `PATCHY_INVESTIGATE_TOKEN_BUDGET`
(`150000`), `PATCHY_REMEDIATE_HARNESS`, `PATCHY_REMEDIATE_MODEL`, `PATCHY_REMEDIATE_TIMEOUT` (`45m`),
`PATCHY_REMEDIATE_MAX_TURNS` (`80`), `PATCHY_REMEDIATE_TOKEN_BUDGET` (`400000`), and `PATCHY_MODEL_ALLOWLIST` (defaults
to the remediate model). The investigate limits are absolute; the remediate max-turns/token-budget values are ceilings
that clamp whatever the investigation report requested — the runner clamps again even though the controller already did.

Two knobs exist only here:

<div class="nowrap-first" markdown>

| Env                          | Default           | Purpose                                                             |
| ---------------------------- | ----------------- | ------------------------------------------------------------------- |
| `PATCHY_CHANGESET_MAX_BYTES` | `5242880` (5 MiB) | Size cap on the changeset's file contents carried out of the pod    |
| `PATCHY_FAKE_FIXTURE`        | —                 | Stream-JSON fixture the `fake` harness replays (tests, dev overlay) |

</div>

Malformed values fail fast with an error naming the exact `PATCHY_<KEY>`.

## The workspace, and how it got there

The pod's **init container** — not the runtime — fetches the repository: `PATCHY_ARTIFACT_URL` points at
source-controller's in-cluster artifact server (an unguessable URL), `PATCHY_ARTIFACT_DIGEST` pins the sha256, and the
init script verifies the digest before extracting to `/workspace/repo` and synthesizing a local git base commit. No
forge credential is involved at any point — `internal/jobs` even lists `GITHUB_TOKEN` as a reserved name so no
configuration can smuggle one in. The per-Job Secret carries only the handoff markdown (`input/issue.md`, plus
`input/investigation.md` for the remediate phase).

## Credentials in the pod

<div class="nowrap-first" markdown>

| Env                       | Source                                                                                                     |
| ------------------------- | ---------------------------------------------------------------------------------------------------------- |
| `ANTHROPIC_API_KEY`       | `patchy-anthropic` Secret via `secretKeyRef` (the default `--anthropic-secret-env`)                        |
| `CLAUDE_CODE_OAUTH_TOKEN` | The same Secret when `--anthropic-secret-env=CLAUDE_CODE_OAUTH_TOKEN` — a `claude setup-token` OAuth token |

</div>

That is the complete list. The agent container's environment passes through to the harness CLI child process, so the
injected key is inherited by `claude` automatically. The `fake` harness needs no credential value — but the Secret must
still exist, because the Job wires the `secretKeyRef` unconditionally.

## The event stream

Progress and results are emitted as one JSON object per line, prefixed `PATCHY-EVENT:`, on stdout; the owning controller
tails the pod log and applies them. Stage outcomes are `ok`, `runtime_error`, `timeout`, `budget_exceeded`,
`report_missing`, `report_invalid`, `commit_failed`, and `changeset_too_large` — only `ok` carries a trusted report. A
fatal error also exits 2 so the Job is marked failed for the controller's orphan handling.
