# agent-runner

The in-pod coding-agent runtime: classification, then remediation, via `claude -p`. It never talks to GitHub, holds no
GitHub credentials, and has no flags — configuration is exclusively `PATCHY_*` environment variables, injected into the
Job pod by the remediation-controller. Results leave the pod as a `PATCHY-EVENT:` JSONL stream on stdout (which is why
all patchy logging goes to stderr).

You normally never configure the agent-runner directly: the remediation-controller's
[stage flags](remediation-controller.md#stage-flags) become this environment. The contract below matters when debugging
a Job spec or running the runtime standalone.

## Identity and phase

<div class="nowrap-first" markdown>

| Env                     | Default              | Purpose                                                     |
| ----------------------- | -------------------- | ----------------------------------------------------------- |
| `PATCHY_REPO`           | — (**required**)     | `owner/name` of the repository under remediation            |
| `PATCHY_ISSUE`          | — (**required**, >0) | The finding's issue number                                  |
| `PATCHY_PHASE`          | `classify+remediate` | `classify+remediate` or `remediate` (the `/approve` re-run) |
| `PATCHY_WORKSPACE`      | `/workspace`         | Pod workspace root (the clone, reports, handoff files)      |
| `PATCHY_DEFAULT_BRANCH` | `main`               | Branch the init container cloned                            |

</div>

## Stage configuration

Mirrors of the controller's stage flags, same defaults: `PATCHY_CLASSIFY_HARNESS`, `PATCHY_CLASSIFY_MODEL`,
`PATCHY_CLASSIFY_TIMEOUT` (`15m`), `PATCHY_CLASSIFY_MAX_TURNS` (`25`), `PATCHY_CLASSIFY_TOKEN_BUDGET` (`150000`),
`PATCHY_REMEDIATE_HARNESS`, `PATCHY_REMEDIATE_MODEL`, `PATCHY_REMEDIATE_TIMEOUT` (`45m`), `PATCHY_REMEDIATE_MAX_TURNS`
(`80`), `PATCHY_REMEDIATE_TOKEN_BUDGET` (`400000`), `PATCHY_CONFIDENCE_THRESHOLD` (`0.75`), and `PATCHY_MODEL_ALLOWLIST`
(defaults to the remediate model). The remediate values are ceilings that clamp whatever the classification report
requests.

Two knobs exist only here:

<div class="nowrap-first" markdown>

| Env                       | Default           | Purpose                                                             |
| ------------------------- | ----------------- | ------------------------------------------------------------------- |
| `PATCHY_BUNDLE_MAX_BYTES` | `5242880` (5 MiB) | Size cap on the git bundle carrying the changeset out of the pod    |
| `PATCHY_FAKE_FIXTURE`     | —                 | Stream-JSON fixture the `fake` harness replays (tests, dev overlay) |

</div>

Malformed values fail fast with an error naming the exact `PATCHY_<KEY>`.

## Credentials in the pod

<div class="nowrap-first" markdown>

| Env                       | Container | Source                                                                                 |
| ------------------------- | --------- | -------------------------------------------------------------------------------------- |
| `GITHUB_TOKEN`            | init only | Per-Job Secret; short-lived, single-repo, read-only clone token. Unset after the clone |
| `ANTHROPIC_API_KEY`       | agent     | `patchy-anthropic` Secret via `secretKeyRef`                                           |
| `CLAUDE_CODE_OAUTH_TOKEN` | agent     | Alternative `claude` CLI credential (checked after the API key)                        |
| `ANTHROPIC_AUTH_TOKEN`    | agent     | Alternative `claude` CLI credential                                                    |

</div>

The agent container's environment is passed through to the harness CLI child process, so the injected key is inherited
by `claude` automatically. The `fake` harness needs no credentials.

## The event stream

Progress and results are emitted as one JSON object per line, prefixed `PATCHY-EVENT:`, on stdout; the
remediation-controller tails the pod log and applies them. Stage outcomes are `ok`, `runtime_error`, `timeout`,
`budget_exceeded`, `report_missing`, `report_invalid`, `commit_failed`, and `bundle_too_large` — anything but `ok`
routes the finding to humans.
