# egress-broker

The egress credential broker: a reconciler-less reverse proxy that agent pods send **all** claude and codex model
traffic through. It is what makes agent pods fully credential-less — the pod carries only an audience-bound projected
ServiceAccount token, and the broker injects or signs the real model credential outbound. It also makes 3rd-party model
providers work: Amazon Bedrock, GCP Vertex AI, and Microsoft Foundry each get a route the claude CLI's documented
gateway mode points at, and Azure OpenAI a route the codex CLI reaches, with the broker holding the cloud identity that
authenticates the provider-native requests.

There is no in-pod credential mode for either harness — claude-without-broker and codex-without-broker are not
configurations. A ChatGPT-plan workspace token (`CODEX_ACCESS_TOKEN` / `CODEX_API_KEY`) cannot be brokered; codex uses
an OpenAI platform API key or Azure OpenAI.

## How a request flows

```text
agent pod (claude CLI)                 egress-broker (release ns)              upstream
  ANTHROPIC_BEDROCK_BASE_URL=            1. TokenReview(X-Patchy-Broker-Token)
    http://…-egress-broker:8080/bedrock  2. audit line (pod, route, path,        bedrock-runtime.<region>.amazonaws.com
  CLAUDE_CODE_SKIP_BEDROCK_AUTH=1           status, duration — never bodies)     api.anthropic.com
  X-Patchy-Broker-Token: <projected      3. strip caller headers; SigV4-sign /   <region>-aiplatform.googleapis.com
    ServiceAccount token>                   inject key / attach OAuth token      <resource>.services.ai.azure.com
                                         4. stream the response back, flushing
                                            every write; SSE keep-alive pings
```

The caller token is validated via `TokenReview` (audience `patchy-egress-broker`; only
`system:serviceaccount:<agent-ns>:<agent-sa>` is accepted), its verdict cached briefly (`--verdict-ttl`, 1m) so the API
server sees one review per token, not one per request, and the header is stripped before anything is forwarded. The
audit identity — the calling pod's name — comes from the pod-bound token's claims.

## Routes and credentials

A route exists exactly when its identifying flag is set; at least one is required.

| Route        | Enable with                | Upstream (default)                               | Credential                                                                                                                                                                                                      |
| ------------ | -------------------------- | ------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `/anthropic` | `--anthropic-api-key-file` | `https://api.anthropic.com`                      | The mounted Secret file, read per request so rotation propagates without a restart; `--anthropic-auth key` injects it as `x-api-key`, `token` as an `Authorization` bearer (a `claude setup-token` OAuth token) |
| `/bedrock`   | `--bedrock-region`         | `https://bedrock-runtime.<region>.amazonaws.com` | SigV4, signed with the broker's ambient AWS identity (IRSA / EKS Pod Identity / env credentials)                                                                                                                |
| `/vertex`    | `--vertex-region`          | `https://<region>-aiplatform.googleapis.com`     | OAuth bearer from Application Default Credentials (GKE Workload Identity in-cluster), cached/refreshed                                                                                                          |
| `/foundry`   | `--foundry-resource`       | `https://<resource>.services.ai.azure.com`       | `--foundry-auth key`: `x-api-key` from a file; `entra`: a cached, auto-refreshed Entra bearer                                                                                                                   |
| `/openai`    | `--openai-api-key-file`    | `https://api.openai.com`                         | (codex) The mounted Secret file, read per request, injected as an `Authorization: Bearer` platform API key                                                                                                      |
| `/azure`     | `--azure-resource`         | `https://<resource>.openai.azure.com/openai`     | (codex) `--azure-auth key`: the `api-key` header from `--azure-api-key-file`; `entra`: a cached, auto-refreshed Entra bearer (Cognitive Services scope, same token source as Foundry)                           |

Cross-cutting behavior: responses stream back with an immediate flush per write; when an event-stream goes quiet for
`--sse-ping-interval` (30s) the broker injects a spec-legal `: ping` comment frame so intermediaries do not idle-close a
long thinking pause; the Bedrock route buffers request bodies (SigV4 signs a payload hash) up to `--max-request-bytes`
(10 MiB). Every caller-supplied credential header (`Authorization`, `x-api-key`, `api-key`, `x-amz-*`) is stripped
before the route attaches its own. Errors are emitted as Anthropic-style JSON error envelopes.

`/healthz` is liveness; `/readyz` additionally requires every configured route's credential source to be usable — a
missing Anthropic or OpenAI key file or unresolvable cloud credentials fail readiness. This replaces the job
controllers' old Secret probing as "is the model credential present": the controllers enable the claude and codex
harnesses by configuration and probe `readyz` advisorily at startup.

## Flags

`--listen-addr` (`:8080`), `--health-addr` (`:8081`), `--token-audience` (`patchy-egress-broker`), `--verdict-ttl`
(`1m`), `--sse-ping-interval` (`30s`; negative disables injection), `--agent-namespace` (`patchy-agents`),
`--agent-service-account` (`patchy-agent`), `--max-request-bytes` (`10485760`), plus the per-route flags above and their
`--<provider>-base-url` overrides. As everywhere, each flag is also the matching `PATCHY_*` environment variable.

## The controller side

The job controllers (and the evaluation controller, via the shared flags) point claude and codex runners at the broker
with `--broker-url` — required whenever a claude or codex runner is configured. Claude's provider is described with
`--claude-provider`, `--claude-provider-region`, `--claude-provider-region-prefix`, `--claude-provider-project-id`,
`--claude-model-map`, and `--claude-provider-env`. Canonical model ids stay canonical everywhere controller-side; the
provider-specific translation (Bedrock `us.anthropic.<model>` inference profiles, Vertex bare ids, Foundry deployment
names) is rendered into `PATCHY_MODEL_MAP` and applied in-pod. Bedrock and Vertex ids are derived per registry model;
**Foundry has no derivable ids**, so `--claude-model-map` must cover every claude-resolving allowlisted model and stage
default — the controllers refuse to start otherwise, which turns a would-be mid-run failure into a startup error. The
map is keyed by **concrete** ids: a family alias such as `anthropic/claude-sonnet` resolves at startup to the newest
registered version (`anthropic/claude-sonnet-5-5`), and that is the key the map must carry. Bedrock and Vertex ids are
derived for unregistered (pattern-tier) versions too, so `anthropic/claude-opus-6` on Bedrock maps to
`us.anthropic.claude-opus-6` with no operator entry.

Codex's provider is described with `--codex-provider` (`openai`, the default, or `azure`), `--codex-model-map`, and
`--codex-provider-env`. `openai` needs no map — the registry already carries codex's ids. **Azure deployment names are
not derivable either**, so on `azure` `--codex-model-map` must cover every codex-resolving allowlisted model and stage
default, with the same startup check.

The codex CLI has no env-only header channel like claude's `ANTHROPIC_CUSTOM_HEADERS`, so the controller renders a codex
`config.toml` into the pod's gateway env (`PATCHY_CODEX_CONFIG`): a custom model provider `patchy-broker` with
`base_url` `<broker>/<route>/v1` (Responses API), `env_key = "PATCHY_BROKER_API_KEY"` (a fixed non-secret placeholder
the broker strips, like claude's first-party one), and `env_http_headers` sending `X-Patchy-Broker-Token` from
`PATCHY_BROKER_TOKEN`; the update check, analytics, and feedback uploads are off. agent-runner writes it to
`${CODEX_HOME:-$HOME/.codex}/config.toml` before every stage and sets the token from the projected file fresh per stage;
evaluation pods do the same once, in the shell wrapper around `evolve exec-unit`.

## Workload identity

For bedrock/vertex/foundry-entra the broker's ServiceAccount is the cloud-identity attachment point, the same recipe as
the context-controller enhancers:

- **AWS (Bedrock)** — IRSA: annotate with `eks.amazonaws.com/role-arn`; or associate EKS Pod Identity and allow the
  agent's link-local endpoint (`169.254.170.23:80`) in `egressBroker.networkPolicy.extraEgress`. Scope the role to
  `bedrock:InvokeModel*`.
- **GCP (Vertex)** — GKE Workload Identity: annotate with `iam.gke.io/gcp-service-account`; the metadata server
  (`169.254.169.254:80`) may need an `extraEgress` rule on clusters that police link-local egress. Grant
  `aiplatform.endpoints.predict` (roles/aiplatform.user).
- **Azure (Foundry or Azure OpenAI, Entra mode)** — Workload Identity: annotate with `azure.workload.identity/client-id`
  and add the `azure.workload.identity/use: "true"` pod label via `egressBroker.podLabels`. Grant Cognitive Services
  User.

Invoke-class permissions only — the broker needs to call models, never to manage them.

## Hardening posture

- RBAC: `create tokenreviews` is the broker's entire Kubernetes surface — no Secret API access (credential files are
  mounted volumes), no writes, no leases.
- The Deployment runs under the restricted Pod Security posture like every other patchy component: non-root 65532,
  read-only root filesystem, no capabilities.
- Its NetworkPolicy admits the proxy port from the agent namespace only, and egress to DNS, TCP 443, and the API server.
- Every request produces one audit log line — caller pod, route, method, path, status, duration, bytes — and never
  bodies or headers.
- In-cluster traffic to the broker is plaintext HTTP carrying an audience-scoped identity token, the same posture as the
  artifact server; broker TLS or mesh mTLS is a hardening follow-up.

## Known limitations

- Brokered codex **evaluations** do not reach the broker yet: evolve gives each run an isolated `CODEX_HOME` and carries
  over only `cli_auth_credentials_store` from the operator's `config.toml`, so the `patchy-broker` provider the wrapper
  writes is dropped. Finding runs (agent-runner) are unaffected. This needs evolve to carry the provider config into its
  per-run home.
- Per-job budget metering at the broker is future work — budgets are enforced in-pod from the harness's streamed usage.
- Bedrock/Vertex runs may report no `total_cost_usd`; cost accounting then falls back to the registry's first-party
  rates, which approximates provider pricing.
