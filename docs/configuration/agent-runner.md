# agent-runner

agent-runner is the coding-agent runtime that runs inside each agent pod. Each Job runs one stage, `investigate` or
`remediate`, through the harness CLI (the coding-agent command-line tool) bundled in its runner image: `claude -p` in
the claude-agent-runner image, `codex exec` in the codex-agent-runner image. It never talks to GitHub or the Kubernetes
API. It has no flags. All of its configuration comes from `PATCHY_*` environment variables, which the job controllers
inject into the Job pod. A claude or codex pod holds **no credential of any kind**. Its model traffic goes through the
[egress broker](egress-broker.md), which authenticates it with a projected ServiceAccount token. Results leave the pod
as a `PATCHY-EVENT:` JSONL stream on stdout, which is why all patchy logging goes to stderr.

You normally never configure the agent-runner directly. The
[investigation-controller](investigation-controller.md#stage-flags) and
[remediation-controller](remediation-controller.md#stage-flags) stage flags become this environment. The contract below
matters when you debug a Job spec or run the runtime on its own.

## Identity and phase

| Env                | Default             | Purpose                                                                                                |
| ------------------ | ------------------- | ------------------------------------------------------------------------------------------------------ |
| `PATCHY_REPO`      | none (**required**) | `owner/name` of the repository under analysis                                                          |
| `PATCHY_FINDING`   | none (**required**) | Name of the owning Finding resource. It is echoed in every event, and the branch is `patchy/<finding>` |
| `PATCHY_BASE_SHA`  | none                | The remote commit the workspace tree corresponds to (the changeset's push base)                        |
| `PATCHY_PHASE`     | `investigate`       | `investigate` or `remediate`                                                                           |
| `PATCHY_WORKSPACE` | `/workspace`        | Pod workspace root (`repo/`, `input/`, `reports/`)                                                     |

## Stage configuration

These variables mirror the controllers' stage flags: `PATCHY_INVESTIGATE_TIMEOUT` (`15m`),
`PATCHY_INVESTIGATE_MAX_TURNS` (`25`), `PATCHY_INVESTIGATE_TOKEN_BUDGET` (`150000`), `PATCHY_REMEDIATE_TIMEOUT` (`45m`),
`PATCHY_REMEDIATE_AUTO_MAX_TURNS` (`80`), `PATCHY_REMEDIATE_AUTO_TOKEN_BUDGET` (`400000`),
`PATCHY_REMEDIATE_MANUAL_MAX_TURNS` (`240`), `PATCHY_REMEDIATE_MANUAL_TOKEN_BUDGET` (`1200000`), and
`PATCHY_MODEL_ALLOWLIST` (canonical model ids, rendered into the analysis prompt).

The controller also sets two variables for each Job: `PATCHY_<STAGE>_HARNESS` and `PATCHY_<STAGE>_MODEL` (a canonical,
provider-qualified model id). They hold the harness and model the controller resolved for this Job. The pod runs the
harness its runner image was built for, on the model the controller chose, and translates the canonical id to the CLI's
own model id.

The investigate limits are absolute. The remediate values work differently: they set a floor and a backstop, and they do
not clamp the budget. Every remediation runs on at least the `AUTO` budget, whatever the investigation estimated. When a
human approved a larger estimate, the per-Job `PATCHY_GRANTED_MAX_TURNS` / `PATCHY_GRANTED_TOKEN_BUDGET` raise that
budget. The `MANUAL` values cap the result. A `MANUAL` value below its `AUTO` value is a configuration error, and the
runner refuses to start. See [The remediation budget](../deployment/models.md#the-remediation-budget).

`PATCHY_CALIBRATION` is the last per-Job variable. It is a JSON summary of how earlier estimates in this repository
compared to what actually happened. It is rendered into the analysis prompt so the next estimate can correct for the
observed skew. It is advisory only. On a cold start it is absent, and the prompt then leaves the section out.

Brokered (claude, codex) Jobs add two more:

| Env                        | Purpose                                                                                                                                                                                                |
| -------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `PATCHY_BROKER_TOKEN_FILE` | Path of the projected ServiceAccount token (`/var/run/patchy/broker/token`). It is read fresh each stage, because the kubelet rotates it, and sent to the broker as the `X-Patchy-Broker-Token` header |
| `PATCHY_MODEL_MAP`         | Comma-joined `canonical=provider-id` pairs. Checked before the registry when translating the stage model to the CLI's `--model` id (Bedrock inference profiles, Foundry/Azure deployment names)        |

On brokered Jobs the controllers also set the claude CLI's gateway environment: `ANTHROPIC_BASE_URL` or the
`CLAUDE_CODE_USE_*` / `CLAUDE_CODE_SKIP_*_AUTH` / `ANTHROPIC_*_BASE_URL` switches, plus
`CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`. These point every model request at the broker's provider route.

On brokered codex Jobs the gateway environment is `PATCHY_CODEX_CONFIG` instead. It holds a rendered codex `config.toml`
that declares a `patchy-broker` model provider at the broker's route. Before each stage, agent-runner writes it to
`${CODEX_HOME:-$HOME/.codex}/config.toml`. It then gives the codex child process two variables:

- `PATCHY_BROKER_TOKEN`: the projected token, read fresh.
- `PATCHY_BROKER_API_KEY`: a fixed placeholder that is not a secret. The broker strips it.

The `patchy-broker` provider reads its header and key from those two names. A brokered codex stage without
`PATCHY_CODEX_CONFIG` fails as a `runtime_error`. All of these names are reserved in `internal/jobs`, so
controller-global configuration can never shadow them.

These knobs exist only here:

| Env                                 | Default           | Purpose                                                             |
| ----------------------------------- | ----------------- | ------------------------------------------------------------------- |
| `PATCHY_CHANGESET_MAX_BYTES`        | `5242880` (5 MiB) | Size cap on the changeset's file contents carried out of the pod    |
| `PATCHY_TRANSCRIPT_MAX_TURN_BYTES`  | `65536` (64 KiB)  | Per-turn text cap on the streamed conversation                      |
| `PATCHY_TRANSCRIPT_MAX_TURNS`       | `0` (unbounded)   | Turn cap on the streamed conversation                               |
| `PATCHY_TRANSCRIPT_MAX_TOTAL_BYTES` | `8388608` (8 MiB) | Total cap on the streamed conversation                              |
| `PATCHY_FAKE_FIXTURE`               | none              | Stream-JSON fixture the `fake` harness replays (tests, dev overlay) |

A malformed value fails fast, with an error that names the `PATCHY_<KEY>` at fault.

The `TRANSCRIPT_*` knobs limit the conversation the pod streams, which is what a live viewer on the status page sees.
They do not limit what is stored. The controller compacts the collected conversation into its ConfigMap separately (500
turns, 2 KiB per turn, 512 KiB total, keeping the head and the tail). Keep the total well under the kubelet's
container-log rotation size (`containerLogMaxSize`, 10 MiB by default). The stage result is read from the current log
file once the agent exits. If the conversation is large enough to rotate the result out of that file, the result is
lost.

## The workspace, and how it got there

The pod's **init container** fetches the repository. The runtime does not. `PATCHY_ARTIFACT_URL` points at
source-controller's in-cluster artifact server (an unguessable URL), and `PATCHY_ARTIFACT_DIGEST` pins the sha256. The
init script verifies the digest, extracts the archive to `/workspace/repo`, and creates a local git base commit. No
forge credential is involved at any point. `internal/jobs` even lists `GITHUB_TOKEN` as a reserved name, so no
configuration can smuggle one in. The per-Job Secret carries only the handoff markdown (`input/issue.md`, plus
`input/investigation.md` for the remediate phase).

## Credentials in the pod

| Harness | In the pod                                                                                                                                                                                 |
| ------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| claude  | **None.** The broker caller token (an identity document rather than a capability) is the pod's only secret material. The model credential lives with the [egress broker](egress-broker.md) |
| codex   | **None**, the same as claude. The pod holds only the broker caller token. The OpenAI / Azure OpenAI credential lives with the [egress broker](egress-broker.md)                            |
| fake    | None. The fixture replay authenticates nothing                                                                                                                                             |

No model credential reaches any pod. The agent container's environment passes through to the harness CLI child process,
so `claude` (or `codex`) inherits the brokered gateway environment automatically. The broker caller token is registered
with the transcript scrubber in the same way as credential values. So a tool result that dumps the environment cannot
leak the token into a saved transcript.

## The event stream

Progress and results are emitted on stdout as one JSON object per line, each prefixed `PATCHY-EVENT:`. The owning
controller tails the pod log and applies them. Stage outcomes are `ok`, `runtime_error`, `timeout`, `budget_exceeded`,
`report_missing`, `report_invalid`, `commit_failed`, and `changeset_too_large`. Only `ok` carries a trusted report. A
fatal error also exits with code 2, so the Job is marked failed and the controller's orphan handling picks it up.
