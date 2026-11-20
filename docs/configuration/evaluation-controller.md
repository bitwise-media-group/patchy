# evaluation-controller

The remote skill-evaluation engine. It is the only **optional** controller: if you never submit remote evaluations, you
do not need to run it. It serves the HTTP API that [evolve](https://github.com/bitwise-media-group/evolve) talks to
(workspace upload, submission, snapshot, SSE monitoring, cancellation). It also runs the reconcilers that:

- expand each submitted `Evaluation` into `EvaluationUnit` children
- schedule the units through the sandboxed agent-Job machinery, with bounded concurrency
- collect each pod's result stream
- expire finished evaluations on a TTL

```sh
evaluation-controller serve --namespace patchy --auth-config /etc/patchy/auth/config.yaml
```

The two sides split the work strictly. evolve owns everything about what an evaluation means: specs, grading, the LLM
judge and baselines. That logic travels with the uploaded workspace bundle into the pod, which runs `evolve exec-unit`
from a per-harness evolve-runner image. Patchy owns scheduling, sandboxing and state. It reads only the pod's `result`
and `fatal` events. It stores the finished results entry as an opaque payload in a per-unit ConfigMap, and the client
puts the results back together locally.

## Flags

The [shared flags](index.md#shared-flags) (`--listen-addr` is the API's own address), plus:

| Flag                           | Env                                 | Default                       | Purpose                                                                                   |
| ------------------------------ | ----------------------------------- | ----------------------------- | ----------------------------------------------------------------------------------------- |
| `--auth-config`                | `PATCHY_AUTH_CONFIG`                | _(required)_                  | Mounted authentication config (see below)                                                 |
| `--max-concurrent-units`       | `PATCHY_MAX_CONCURRENT_UNITS`       | `4`                           | Evaluation units running at once, across all submissions                                  |
| `--max-units-per-evaluation`   | `PATCHY_MAX_UNITS_PER_EVALUATION`   | `200`                         | Largest accepted submission, in units                                                     |
| `--max-submission-bytes`       | `PATCHY_MAX_SUBMISSION_BYTES`       | `8388608`                     | Largest accepted submission body                                                          |
| `--max-workspace-bytes`        | `PATCHY_MAX_WORKSPACE_BYTES`        | `67108864`                    | Largest accepted workspace bundle                                                         |
| `--evaluation-ttl`             | `PATCHY_EVALUATION_TTL`             | `72h`                         | Retention of finished evaluations (`spec.ttlSecondsAfterFinished` overrides; `0` forever) |
| `--agent-namespace`            | `PATCHY_AGENT_NAMESPACE`            | `patchy-agents`               | Namespace evaluation Jobs run in                                                          |
| `--agent-service-account`      | `PATCHY_AGENT_SERVICE_ACCOUNT`      | `patchy-agent`                | Service account evaluation Jobs run as                                                    |
| `--job-deadline`               | `PATCHY_JOB_DEADLINE`               | `2h`                          | `activeDeadlineSeconds` on evaluation Jobs                                                |
| `--job-ttl`                    | `PATCHY_JOB_TTL`                    | `1h`                          | `ttlSecondsAfterFinished` on evaluation Jobs                                              |
| `--artifact-base-url`          | `PATCHY_ARTIFACT_BASE_URL`          | source-controller svc `:9790` | Artifact endpoint agent pods fetch workspace bundles from                                 |
| `--artifact-upload-url`        | `PATCHY_ARTIFACT_UPLOAD_URL`        | source-controller svc `:9791` | source-controller's internal blob endpoint uploads stream to                              |
| `--internal-upload-token-file` | `PATCHY_INTERNAL_UPLOAD_TOKEN_FILE` | none                          | Shared bearer token for the internal blob endpoint (defense-in-depth atop NetworkPolicy)  |
| `--evolve-claude-image`        | `PATCHY_EVOLVE_CLAUDE_IMAGE`        | none                          | evolve-runner image with the claude CLI; unset disables the claude evaluation runner      |
| `--evolve-codex-image`         | `PATCHY_EVOLVE_CODEX_IMAGE`         | none                          | evolve-runner image with the codex CLI                                                    |
| `--evolve-fake-image`          | `PATCHY_EVOLVE_FAKE_IMAGE`          | none                          | Fake evolve runner for dev/e2e (replays fixtures, no credential)                          |
| `--harnesses`                  | `PATCHY_HARNESSES`                  | none (auto)                   | Restrict enabled harnesses (default: every harness whose evolve image is set)             |

The claude and codex evolve runners go through the broker, the same as the runners that work on findings. They reuse the
shared `--broker-url` and `--<harness>-provider*` / `--<harness>-model-map` flags (see
[investigation-controller](investigation-controller.md)). There is one [egress broker](egress-broker.md) route per model
vendor, shared by finding runs and evaluation runs, and the pod holds no credential. A brokered eval pod runs
`evolve exec-unit` inside a shell wrapper. The wrapper captures the projected caller token once, at start. For claude it
sets `ANTHROPIC_CUSTOM_HEADERS`. For codex it also writes the rendered `PATCHY_CODEX_CONFIG` to
`${CODEX_HOME:-$HOME/.codex}/config.toml`.

!!! warning "Brokered codex evaluations need an evolve update"

    evolve runs codex with an isolated `CODEX_HOME` for each run. It copies only `cli_auth_credentials_store` from the
    operator's `config.toml`, so it drops the broker provider that the wrapper writes. Until evolve copies that provider
    config into its per-run home, codex evaluation runs cannot reach the broker. Claude evaluations are not affected.

The evolve-runner images ship from the evolve repository (`ghcr.io/bitwise-media-group/evolve-runner-<harness>`). The
operator pins them, the same way as the agent images. The controller records each submission's `clientVersion`. A
version mismatch produces a warning but never blocks the submission.

## Authentication configuration

`--auth-config` points at a YAML file, conventionally a mounted Secret (`patchy-evaluation-auth`, key `config.yaml`).
Unlike the status server, there is **no unauthenticated mode**. Every route except `GET /api/v1/auth/info` needs a
bearer token, so a missing or invalid file is a startup error.

```yaml
mode: oidc # none | oidc
oidc: # mode: oidc only
  issuerURL: https://sso.example.com
  clientID: evolve # the PUBLIC (PKCE) client evolve logs in as; it has no client secret
  # scopes: [openid, offline_access, profile, email, groups]
  claims: # claim NAMES, mapped onto the identity
    username: email
    groups: groups
```

`mode: none` (dev/e2e) skips authentication, uses a fixed identity, and skips authorization entirely.

`evolve login` needs no OIDC configuration on the client side. It reads `GET /api/v1/auth/info` (issuer, clientID,
scopes) and runs the authorization-code + PKCE flow against the issuer with a localhost redirect. As the operator, you
need a **public** OIDC client (no secret) that allows `http://127.0.0.1:*/callback` redirects.

## Authorization

Authorization is native Kubernetes RBAC on the `evaluations` resource and nothing else. There are no custom verbs and no
admission policy:

| Verb     | Grants                                          |
| -------- | ----------------------------------------------- |
| `create` | Submit evaluations and upload workspace bundles |
| `get`    | Read snapshots and stream the SSE monitor       |
| `delete` | Cancel an evaluation                            |

Bind users or SSO groups (as mapped by the claims configuration) to a role with those verbs. The kustomize base ships an
example tier (`patchy-evaluations-submitter` in `rbac.users.example.yaml`). The chart renders one when
`evaluationController.rbac.userRoles` is set.

## Workspace bundles

Bundles are addressed by their sha256 digest:

- `HEAD /api/v1/workspaces/{digest}` checks for an existing copy, so the client can skip a duplicate upload.
- `PUT /api/v1/workspaces/{digest}` streams the gzip tarball through to source-controller's internal endpoint (`:9791`),
  which a NetworkPolicy guards. That endpoint verifies the digest before caching the bundle.

The digest is also what lets the agent pod download the bundle: knowing it is enough to fetch it. Bundles unused for
`--workspace-retention` (a source-controller flag, default `168h`) are removed. If a unit needs a bundle that was
removed, the unit ends as `WorkspaceLost`, and the client uploads and submits again.
