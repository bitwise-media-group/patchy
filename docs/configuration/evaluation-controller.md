# evaluation-controller

The remote skill-evaluation engine, and the one **optional** controller: deployments that never submit remote
evaluations simply do not run it. It serves the [evolve](https://github.com/bitwise-media-group/evolve)-facing HTTP API
(workspace upload, submission, snapshot, SSE monitoring, cancellation) and runs the reconcilers that expand each
submitted `Evaluation` into `EvaluationUnit` children, schedule them through the sandboxed agent-Job machinery with
bounded concurrency, collect each pod's result stream, and expire finished evaluations on a TTL.

```sh
evaluation-controller serve --namespace patchy --auth-config /etc/patchy/auth/config.yaml
```

The division of knowledge is strict: evolve owns every evaluation semantic (specs, grading, the LLM judge, baselines),
co-located with the uploaded workspace bundle inside the pod, which runs `evolve exec-unit` from a per-harness
evolve-runner image. Patchy owns scheduling, sandboxing, and state — it interprets only the pod's `result` and `fatal`
events; the finished results entry is opaque payload stored in a per-unit ConfigMap the client reassembles locally.

## Flags

The [shared flags](index.md#shared-flags-all-five-controllers) (`--listen-addr` is the API's own address), plus:

| Flag                           | Env                                 | Default                       | Purpose                                                                                    |
| ------------------------------ | ----------------------------------- | ----------------------------- | ------------------------------------------------------------------------------------------ |
| `--namespace`                  | `PATCHY_NAMESPACE`                  | `POD_NAMESPACE`               | Namespace the Evaluations live in                                                          |
| `--kubeconfig`                 | `PATCHY_KUBECONFIG`                 | in-cluster                    | Kubeconfig path for running outside the cluster                                            |
| `--health-addr`                | `PATCHY_HEALTH_ADDR`                | `:8081`                       | healthz/readyz probe listen address                                                        |
| `--auth-config`                | `PATCHY_AUTH_CONFIG`                | _(required)_                  | Mounted authentication config (see below)                                                  |
| `--max-concurrent-units`       | `PATCHY_MAX_CONCURRENT_UNITS`       | `4`                           | Evaluation units running at once, across all submissions                                   |
| `--max-units-per-evaluation`   | `PATCHY_MAX_UNITS_PER_EVALUATION`   | `200`                         | Largest accepted submission, in units                                                      |
| `--max-submission-bytes`       | `PATCHY_MAX_SUBMISSION_BYTES`       | `8388608`                     | Largest accepted submission body                                                           |
| `--max-workspace-bytes`        | `PATCHY_MAX_WORKSPACE_BYTES`        | `67108864`                    | Largest accepted workspace bundle                                                          |
| `--evaluation-ttl`             | `PATCHY_EVALUATION_TTL`             | `72h`                         | Retention of finished evaluations (`spec.ttlSecondsAfterFinished` overrides; `0` forever)  |
| `--agent-namespace`            | `PATCHY_AGENT_NAMESPACE`            | `patchy-agents`               | Namespace evaluation Jobs run in                                                           |
| `--agent-service-account`      | `PATCHY_AGENT_SERVICE_ACCOUNT`      | `patchy-agent`                | Service account evaluation Jobs run as                                                     |
| `--job-deadline`               | `PATCHY_JOB_DEADLINE`               | `2h`                          | `activeDeadlineSeconds` on evaluation Jobs                                                 |
| `--job-ttl`                    | `PATCHY_JOB_TTL`                    | `1h`                          | `ttlSecondsAfterFinished` on evaluation Jobs                                               |
| `--artifact-base-url`          | `PATCHY_ARTIFACT_BASE_URL`          | source-controller svc `:9790` | Artifact endpoint agent pods fetch workspace bundles from                                  |
| `--artifact-upload-url`        | `PATCHY_ARTIFACT_UPLOAD_URL`        | source-controller svc `:9791` | source-controller's internal blob endpoint uploads stream to                               |
| `--internal-upload-token-file` | `PATCHY_INTERNAL_UPLOAD_TOKEN_FILE` | _(unset)_                     | Shared bearer token for the internal blob endpoint (defense-in-depth atop NetworkPolicy)   |
| `--evolve-claude-image`        | `PATCHY_EVOLVE_CLAUDE_IMAGE`        | _(unset)_                     | evolve-runner image with the claude CLI; unset disables the claude evaluation runner       |
| `--evolve-codex-image`         | `PATCHY_EVOLVE_CODEX_IMAGE`         | _(unset)_                     | evolve-runner image with the codex CLI                                                     |
| `--evolve-copilot-image`       | `PATCHY_EVOLVE_COPILOT_IMAGE`       | _(unset)_                     | evolve-runner image with the copilot CLI                                                   |
| `--evolve-fake-image`          | `PATCHY_EVOLVE_FAKE_IMAGE`          | _(unset)_                     | Fake evolve runner for dev/e2e (replays fixtures, no credential)                           |
| `--harnesses`                  | `PATCHY_HARNESSES`                  | _(auto)_                      | Restrict enabled harnesses (default: any configured runner whose credential Secret exists) |

Credentials reuse the shared `--<harness>-secret{,-key,-env}` flags
([investigation-controller](investigation-controller.md)): one Secret per model vendor in the agents namespace,
whichever fleet exercises it. The evolve-runner images ship from the evolve repository
(`ghcr.io/bitwise-media-group/evolve-runner-<harness>`), pinned by the operator like today's agent images; a
submission's `clientVersion` is recorded and skew is warned, never enforced.

## Authentication configuration

`--auth-config` points at a YAML file, conventionally a mounted Secret (`patchy-evaluation-auth`, key `config.yaml`).
Unlike the status server there is **no unauthenticated posture** — every route except `GET /api/v1/auth/info` is
bearer-authenticated, so a missing or invalid file is a startup error.

```yaml
mode: oidc # none | oidc
oidc: # mode: oidc only
  issuerURL: https://sso.example.com
  clientID: evolve # the PUBLIC (PKCE) client evolve logs in as — no client secret
  # scopes: [openid, offline_access, profile, email, groups]
  claims: # claim NAMES, mapped onto the identity
    username: email
    groups: groups
```

`mode: none` (dev/e2e) short-circuits authentication with a fixed identity and bypasses authorization entirely.

`evolve login` needs zero OIDC configuration client-side: it reads `GET /api/v1/auth/info` (issuer, clientID, scopes)
and runs the authorization-code + PKCE flow against the issuer with a localhost redirect. The operator prerequisite is a
**public** OIDC client (no secret) allowing `http://127.0.0.1:*/callback` redirects.

## Authorization

Native Kubernetes RBAC on the `evaluations` resource is the entire authorization surface — no custom verbs, no
admission-policy involvement:

| Verb     | Grants                                          |
| -------- | ----------------------------------------------- |
| `create` | Submit evaluations and upload workspace bundles |
| `get`    | Read snapshots and stream the SSE monitor       |
| `delete` | Cancel an evaluation                            |

Bind users or SSO groups (as mapped by the claims configuration) to a role carrying those verbs — the kustomize base
ships an example tier (`patchy-evaluations-submitter` in `rbac.users.example.yaml`), and the chart renders one behind
`evaluationController.rbac.userRoles`.

## Workspace bundles

Bundles are content-addressed by sha256: `HEAD /api/v1/workspaces/{digest}` dedupes before upload,
`PUT /api/v1/workspaces/{digest}` streams the gzip tarball through to source-controller's NetworkPolicy-gated internal
endpoint (`:9791`), which verifies the digest before caching. The digest is also the fetch capability the agent pod
downloads the bundle with. Bundles unused for `--workspace-retention` (source-controller, default `168h`) are swept; a
needed-but-swept bundle settles its unit as `WorkspaceLost`, prompting the client to re-upload and resubmit.
