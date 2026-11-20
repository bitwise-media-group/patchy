# egress-broker

The egress credential broker is a reverse proxy with no reconcilers. Agent pods send **all** claude and codex model
traffic through it. It is what keeps agent pods completely free of credentials. The pod carries only a projected
ServiceAccount token bound to the broker's audience, and the broker injects or signs the real model credential on the
way out.

The broker also makes third-party model providers work:

- Amazon Bedrock, GCP Vertex AI, and Microsoft Foundry each get a route that the claude CLI's documented gateway mode
  points at.
- Azure OpenAI gets a route that the codex CLI reaches.

The broker holds the cloud identity that authenticates these provider-native requests.

Neither harness has an in-pod credential mode. Running claude or codex without the broker is not a supported
configuration. A ChatGPT-plan workspace token (`CODEX_ACCESS_TOKEN` / `CODEX_API_KEY`) cannot be brokered, so codex uses
an OpenAI platform API key or Azure OpenAI.

## How a request flows

```text
agent pod (claude CLI)                 egress-broker (release ns)              upstream
  ANTHROPIC_BEDROCK_BASE_URL=            1. TokenReview(X-Patchy-Broker-Token)
    http://…-egress-broker:8080/bedrock  2. audit line (pod, route, path,        bedrock-runtime.<region>.amazonaws.com
  CLAUDE_CODE_SKIP_BEDROCK_AUTH=1           status, duration; no bodies)        api.anthropic.com
  X-Patchy-Broker-Token: <projected      3. strip caller headers; SigV4-sign /   <region>-aiplatform.googleapis.com
    ServiceAccount token>                   inject key / attach OAuth token      <resource>.services.ai.azure.com
                                         4. stream the response back, flushing
                                            every write; SSE keep-alive pings
```

The broker validates the caller token with a `TokenReview` (audience `patchy-egress-broker`). It accepts only
`system:serviceaccount:<agent-ns>:<agent-sa>`. It caches the verdict briefly (`--verdict-ttl`, 1m), so the API server
sees one review per token instead of one per request. It strips the header before forwarding anything. The audit
identity (the calling pod's name) comes from the pod-bound token's claims.

## Routes and credentials

A route exists when its identifying flag is set, and only then. At least one route is required.

| Route        | Enable with                | Upstream (default)                               | Credential                                                                                                                                                                                                      |
| ------------ | -------------------------- | ------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `/anthropic` | `--anthropic-api-key-file` | `https://api.anthropic.com`                      | The mounted Secret file, read per request so rotation propagates without a restart; `--anthropic-auth key` injects it as `x-api-key`, `token` as an `Authorization` bearer (a `claude setup-token` OAuth token) |
| `/bedrock`   | `--bedrock-region`         | `https://bedrock-runtime.<region>.amazonaws.com` | SigV4, signed with the broker's ambient AWS identity (IRSA / EKS Pod Identity / env credentials)                                                                                                                |
| `/vertex`    | `--vertex-region`          | `https://<region>-aiplatform.googleapis.com`     | OAuth bearer from Application Default Credentials (GKE Workload Identity in-cluster), cached/refreshed                                                                                                          |
| `/foundry`   | `--foundry-resource`       | `https://<resource>.services.ai.azure.com`       | `--foundry-auth key`: `x-api-key` from `--foundry-api-key-file`; `entra`: a cached, auto-refreshed Entra bearer                                                                                                 |
| `/openai`    | `--openai-api-key-file`    | `https://api.openai.com`                         | (codex) The mounted Secret file, read per request, injected as an `Authorization: Bearer` platform API key                                                                                                      |
| `/azure`     | `--azure-resource`         | `https://<resource>.openai.azure.com/openai`     | (codex) `--azure-auth key`: the `api-key` header from `--azure-api-key-file`; `entra`: a cached, auto-refreshed Entra bearer (Cognitive Services scope, same token source as Foundry)                           |

All routes share this behavior:

- Responses stream back, with an immediate flush on each write.
- When an event stream goes quiet for `--sse-ping-interval` (30s), the broker injects a spec-legal `: ping` comment
  frame. This stops intermediaries from closing an idle connection during a long thinking pause.
- The Bedrock route buffers request bodies up to `--max-request-bytes` (10 MiB), because SigV4 signs a payload hash.
- Every caller-supplied credential header (`Authorization`, `x-api-key`, `api-key`, `x-amz-*`) is stripped before the
  route attaches its own.
- Errors are returned as Anthropic-style JSON error envelopes.

`/healthz` is liveness. `/readyz` also requires every configured route's credential source to be usable, so a missing
Anthropic or OpenAI key file, or cloud credentials that cannot be resolved, fail readiness. This replaces the job
controllers' old Secret probing as the check for "is the model credential present". The controllers enable the claude
and codex harnesses from configuration. At startup they probe `readyz`, but only to log a warning.

## Flags

`--listen-addr` (`:8080`), `--health-addr` (`:8081`), `--token-audience` (`patchy-egress-broker`), `--verdict-ttl`
(`1m`), `--sse-ping-interval` (`30s`; negative disables injection), `--agent-namespace` (`patchy-agents`),
`--agent-service-account` (`patchy-agent`), `--max-request-bytes` (`10485760`), plus the per-route flags above and their
`--<provider>-base-url` overrides. As everywhere, you can also set each flag with the matching `PATCHY_*` environment
variable.

## The controller side

The job controllers point claude and codex runners at the broker with `--broker-url`. The evaluation controller does the
same through the shared flags. `--broker-url` is required whenever a claude or codex runner is configured.

Claude's provider is set with `--claude-provider`, `--claude-provider-region`, `--claude-provider-region-prefix`,
`--claude-provider-project-id`, `--claude-model-map`, and `--claude-provider-env`. On the controller side, model ids
stay canonical everywhere. The provider-specific translation (Bedrock `us.anthropic.<model>` inference profiles, Vertex
bare ids, Foundry deployment names) is rendered into `PATCHY_MODEL_MAP` and applied inside the pod.

- Bedrock and Vertex ids are derived for each registry model.
- **Foundry ids cannot be derived**, so `--claude-model-map` must cover every allowlisted model and stage default that
  resolves to claude. Otherwise the controllers refuse to start. This turns a failure that would happen mid-run into a
  startup error.

The map is keyed by **concrete** ids. A family alias such as `anthropic/claude-sonnet` resolves at startup to the newest
registered version (`anthropic/claude-sonnet-5-5`), and that is the key the map must hold. Bedrock and Vertex ids are
derived for unregistered (pattern-tier) versions too. So `anthropic/claude-opus-6` on Bedrock maps to
`us.anthropic.claude-opus-6` without an operator entry.

Codex's provider is set with `--codex-provider` (`openai`, the default, or `azure`), `--codex-model-map`, and
`--codex-provider-env`. `openai` needs no map, because the registry already holds codex's ids. **Azure deployment names
cannot be derived either.** So on `azure`, `--codex-model-map` must cover every allowlisted model and stage default that
resolves to codex. The same startup check applies.

The codex CLI has no environment-only header channel like claude's `ANTHROPIC_CUSTOM_HEADERS`. So the controller renders
a codex `config.toml` into the pod's gateway environment (`PATCHY_CODEX_CONFIG`). It defines a custom model provider,
`patchy-broker`, with:

- `base_url` `<broker>/<route>/v1` (Responses API).
- `env_key = "PATCHY_BROKER_API_KEY"`: a fixed placeholder that is not a secret. The broker strips it, as it does
  claude's first-party one.
- `env_http_headers`, which sends `X-Patchy-Broker-Token` from `PATCHY_BROKER_TOKEN`.

The update check, analytics, and feedback uploads are turned off. agent-runner writes the file to
`${CODEX_HOME:-$HOME/.codex}/config.toml` before every stage, and sets the token fresh from the projected file each
stage. Evaluation pods do the same once, in the shell wrapper around `evolve exec-unit`.

## Workload identity

For bedrock, vertex, and foundry in entra mode, the cloud identity attaches to the broker's ServiceAccount. The recipe
is the same as for the context-controller enhancers:

- **AWS (Bedrock)**: use IRSA by annotating with `eks.amazonaws.com/role-arn`. Or associate EKS Pod Identity and allow
  the agent's link-local endpoint (`169.254.170.23:80`) in `egressBroker.networkPolicy.extraEgress`. Scope the role to
  `bedrock:InvokeModel*`.
- **GCP (Vertex)**: use GKE Workload Identity by annotating with `iam.gke.io/gcp-service-account`. On clusters that
  police link-local egress, the metadata server (`169.254.169.254:80`) may need an `extraEgress` rule. Grant
  `aiplatform.endpoints.predict` (roles/aiplatform.user).
- **Azure (Foundry or Azure OpenAI, Entra mode)**: use Workload Identity. Annotate with
  `azure.workload.identity/client-id` and add the `azure.workload.identity/use: "true"` pod label through
  `egressBroker.podLabels`. Grant Cognitive Services User.

Grant only invoke-class permissions. The broker needs to call models, never to manage them.

## Hardening posture

- RBAC: `create tokenreviews` is the broker's only Kubernetes permission. It has no Secret API access (credential files
  are mounted volumes), no writes, and no leases.
- The Deployment runs under the restricted Pod Security standard like every other patchy component: non-root 65532,
  read-only root filesystem, no capabilities.
- Its NetworkPolicy admits the proxy port from the agent namespace only, and egress to DNS, TCP 443, and the API server.
- Every request produces one audit log line (caller pod, route, method, path, status, duration, bytes). It never
  includes bodies or headers.
- In-cluster traffic to the broker is plaintext HTTP carrying an audience-scoped identity token. The artifact server has
  the same posture. Broker TLS or mesh mTLS is left for a later hardening step.

## Known limitations

- Brokered codex **evaluations** cannot reach the broker yet. evolve gives each run an isolated `CODEX_HOME` and copies
  over only `cli_auth_credentials_store` from the operator's `config.toml`. So it drops the `patchy-broker` provider the
  wrapper writes. Finding runs (agent-runner) are not affected. A fix needs evolve to carry the provider config into its
  per-run home.
- Per-job budget metering at the broker is future work. Budgets are enforced inside the pod from the harness's streamed
  usage.
- Bedrock/Vertex runs may report no `total_cost_usd`; cost accounting then falls back to the registry's first-party
  rates, which approximates provider pricing.
