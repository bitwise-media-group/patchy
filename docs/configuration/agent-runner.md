# agent-runner

The in-pod coding-agent runtime: one stage per Job — `investigate` or `remediate` — via the harness CLI its runner image
bundles (`claude -p` in the claude-agent-runner image, `codex exec` in the codex-agent-runner image, `copilot -p` in the
copilot-agent-runner image). It never talks to GitHub or the Kubernetes API, and it has no flags — configuration is
exclusively `PATCHY_*` environment variables, injected into the Job pod by the job controllers. A claude pod holds **no
credential of any kind**: its model traffic goes through the [egress broker](egress-broker.md), authenticated by a
projected ServiceAccount token; a codex or copilot pod holds the one model key of its harness. Results leave the pod as
a `PATCHY-EVENT:` JSONL stream on stdout (which is why all patchy logging goes to stderr).

You normally never configure the agent-runner directly: the
[investigation-controller](investigation-controller.md#stage-flags) and
[remediation-controller](remediation-controller.md#stage-flags) stage flags become this environment. The contract below
matters when debugging a Job spec or running the runtime standalone.

## Identity and phase

| Env                | Default          | Purpose                                                                                           |
| ------------------ | ---------------- | ------------------------------------------------------------------------------------------------- |
| `PATCHY_REPO`      | — (**required**) | `owner/name` of the repository under analysis                                                     |
| `PATCHY_FINDING`   | — (**required**) | Name of the owning Finding resource — echoed in every event, and the branch is `patchy/<finding>` |
| `PATCHY_BASE_SHA`  | —                | The remote commit the workspace tree corresponds to (the changeset's push base)                   |
| `PATCHY_PHASE`     | `investigate`    | `investigate` or `remediate`                                                                      |
| `PATCHY_WORKSPACE` | `/workspace`     | Pod workspace root (`repo/`, `input/`, `reports/`)                                                |

## Stage configuration

Mirrors of the controllers' stage flags: `PATCHY_INVESTIGATE_TIMEOUT` (`15m`), `PATCHY_INVESTIGATE_MAX_TURNS` (`25`),
`PATCHY_INVESTIGATE_TOKEN_BUDGET` (`150000`), `PATCHY_REMEDIATE_TIMEOUT` (`45m`), `PATCHY_REMEDIATE_AUTO_MAX_TURNS`
(`80`), `PATCHY_REMEDIATE_AUTO_TOKEN_BUDGET` (`400000`), `PATCHY_REMEDIATE_MANUAL_MAX_TURNS` (`240`),
`PATCHY_REMEDIATE_MANUAL_TOKEN_BUDGET` (`1200000`), and `PATCHY_MODEL_ALLOWLIST` (canonical model ids, rendered into the
analysis prompt). The **per-Job** `PATCHY_<STAGE>_HARNESS` and `PATCHY_<STAGE>_MODEL` (a canonical, provider-qualified
id) are set by the controller from the harness and model it resolved for this Job — so the pod runs the harness its
runner image was built for on the model the controller chose, and translates that canonical id to the CLI's own model
id. The investigate limits are absolute. The remediate values are a floor and a backstop, not a clamp: every remediation
runs on at least the ceiling whatever the investigation estimated, the per-Job `PATCHY_GRANTED_MAX_TURNS` /
`PATCHY_GRANTED_TOKEN_BUDGET` raise that when a human approved a larger estimate, and the `_HARD` values bound the
result. A hard cap below its ceiling is a configuration error and the runner refuses to start.

`PATCHY_CALIBRATION` is the last per-Job variable: a JSON summary of how earlier estimates in this repository compared
to reality, rendered into the analysis prompt so the next estimate can correct for the observed skew. It is advisory —
absent on a cold start, and the prompt then omits the section entirely.

Brokered (claude) Jobs add two more:

| Env                        | Purpose                                                                                                                                                                                     |
| -------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `PATCHY_BROKER_TOKEN_FILE` | Path of the projected ServiceAccount token (`/var/run/patchy/broker/token`); read fresh each stage — the kubelet rotates it — and sent to the broker as the `X-Patchy-Broker-Token` header  |
| `PATCHY_MODEL_MAP`         | Comma-joined `canonical=provider-id` pairs; consulted before the registry when translating the stage model to the CLI's `--model` id (Bedrock inference profiles, Foundry deployment names) |

The controllers also set the claude CLI's gateway environment on brokered Jobs — `ANTHROPIC_BASE_URL` or the
`CLAUDE_CODE_USE_*` / `CLAUDE_CODE_SKIP_*_AUTH` / `ANTHROPIC_*_BASE_URL` switches, plus
`CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1` — pointing every model request at the broker's provider route. All of these
names are reserved in `internal/jobs`, so controller-global configuration can never shadow them.

Two knobs exist only here:

| Env                                 | Default            | Purpose                                                             |
| ----------------------------------- | ------------------ | ------------------------------------------------------------------- |
| `PATCHY_CHANGESET_MAX_BYTES`        | `5242880` (5 MiB)  | Size cap on the changeset's file contents carried out of the pod    |
| `PATCHY_TRANSCRIPT_MAX_TURN_BYTES`  | `2048`             | Per-turn text cap in the captured conversation                      |
| `PATCHY_TRANSCRIPT_MAX_TURNS`       | `500`              | Turn cap for one run's conversation                                 |
| `PATCHY_TRANSCRIPT_MAX_TOTAL_BYTES` | `524288` (512 KiB) | Total cap on one run's conversation, before compression             |
| `PATCHY_FAKE_FIXTURE`               | —                  | Stream-JSON fixture the `fake` harness replays (tests, dev overlay) |

Malformed values fail fast with an error naming the exact `PATCHY_<KEY>`.

## The workspace, and how it got there

The pod's **init container** — not the runtime — fetches the repository: `PATCHY_ARTIFACT_URL` points at
source-controller's in-cluster artifact server (an unguessable URL), `PATCHY_ARTIFACT_DIGEST` pins the sha256, and the
init script verifies the digest before extracting to `/workspace/repo` and synthesizing a local git base commit. No
forge credential is involved at any point — `internal/jobs` even lists `GITHUB_TOKEN` as a reserved name so no
configuration can smuggle one in. The per-Job Secret carries only the handoff markdown (`input/issue.md`, plus
`input/investigation.md` for the remediate phase).

## Credentials in the pod

| Harness | In the pod                                                                                                                                                                          |
| ------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| claude  | **None.** The broker caller token (an identity document, not a capability) is the pod's only secret material; the model credential lives with the [egress broker](egress-broker.md) |
| codex   | `OPENAI_API_KEY` via `secretKeyRef` (`--codex-secret`; `CODEX_API_KEY` / `CODEX_ACCESS_TOKEN` when `--codex-secret-env` names one)                                                  |
| copilot | `COPILOT_GITHUB_TOKEN` via `secretKeyRef` (`--copilot-secret`) — a GitHub token, not a model API key; `GH_TOKEN` / `GITHUB_TOKEN` when `--copilot-secret-env` names one             |
| fake    | None — the fixture replay authenticates nothing                                                                                                                                     |

At most **one** credential reaches a given pod: the Job wires the `secretKeyRef` of the harness it runs, so a codex Job
carries only the OpenAI credential. The agent container's environment passes through to the harness CLI child process,
so an injected key — or the brokered gateway environment — is inherited by `claude` (or `codex`, or `copilot`)
automatically. The broker caller token is registered with the transcript scrubber the same way credential values are, so
a tool result that dumps the environment cannot leak it into a persisted transcript.

The copilot rows are the exception to "no forge credential ever reaches the pod": the Copilot CLI authenticates with a
GitHub token, so a copilot Job does carry one. It is a model credential by role, not a forge one — the runner passes
`--disable-builtin-mcps`, so no tool in the session speaks the GitHub API, and the harness's egress policy admits only
`api.github.com` (the token exchange) and the `*.githubcopilot.com` inference endpoints. patchy's own forge traffic is
still controller-side only. Scope the token to Copilot with no repository permissions, and note the copilot runner ships
disabled for exactly this reason.

## The event stream

Progress and results are emitted as one JSON object per line, prefixed `PATCHY-EVENT:`, on stdout; the owning controller
tails the pod log and applies them. Stage outcomes are `ok`, `runtime_error`, `timeout`, `budget_exceeded`,
`report_missing`, `report_invalid`, `commit_failed`, and `changeset_too_large` — only `ok` carries a trusted report. A
fatal error also exits 2 so the Job is marked failed for the controller's orphan handling.
