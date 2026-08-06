# Extending

Patchy ships GHAS/CodeQL, Google Cloud Security Command Center and Wiz support plus a placeholder context enhancer, but
both ends of the pipeline are plugin seams. The public interfaces live under `pkg/` — the only packages whose signatures
are stable for external reuse — and the built-in implementations under `internal/ghas`, `internal/scc`, `internal/wiz`
and `internal/enhancers` are reference implementations of the same interfaces.

There are two ways in. An **in-tree plugin** implements the Go interfaces below and is compiled into the controllers. An
**external process** implements the [generic HTTP contract](integrations/generic.md) instead — the same source,
write-back, and enhancer seams spoken over signed HTTP by a `provider: generic` Integration, with no patchy code changed
at all. Reach for generic first when the integration is yours to host; reach for an in-tree plugin when it should ship
with patchy.

## Finding sources (`pkg/source`)

A source turns an external tool's alerts into patchy findings: it parses the delivery, fetches whatever detail the
tool's API offers, and hands the **integration-controller** a normalised finding — identifiers (CWE/CVE/GHSA, most
authoritative first; the primary one keys accumulation), severity, locations, and the evidence that becomes the
Finding's description and the tracking issue's body. The built-in `ghas` handler does exactly this for
`code_scanning_alert` deliveries; `gcp-scc` does it for Security Command Center notifications, which need no API call at
all because the notification carries everything.

The design intent (see `DESIGN.md`) is that SAST tools, dependency scanners, cloud posture tools, or even agentic
reviewers plug in here without touching the accumulation, projection, or remediation machinery — the `Finding` schema is
source-agnostic, and `spec.source` (projected as the `security-source` label) records where a finding came from.

**Not every finding is about code.** A finding may name a repository, a `CloudResource`, or both, and that choice
decides how it accumulates: code findings group per repository, cloud findings per resource. A finding without a
repository is legal — it flows through triage and is handed to a human rather than remediated — and it may acquire one
later, from an enhancer. What is not legal is a finding naming neither: there would be nothing to accumulate it against,
so ingest rejects it.

A source may also implement `source.Resolver`, the optional write-back: telling the originating tool what patchy
decided, so a finding dismissed here does not stay open there. `ghas` implements it by dismissing the code-scanning
alert. The pipeline groups a finding's alerts by the source recorded on each one and hands each source only its own, so
provenance is a fact carried from ingest rather than something inferred from the shape of an identifier. A source that
implements no write-back is simply skipped — reading is a complete source.

### Adding a provider webhook

Each provider gets one `webhook.Endpoint` on the single internet-facing listener, supplying the two things that vary: an
`Authenticator` (how a delivery proves it is genuine) and a `Decoder` (where its event type and delivery id live).
GitHub signs an HMAC over the raw body and labels deliveries with headers; a Pub/Sub push cannot compute an HMAC at all
— Pub/Sub composes the message, so the sender never sees the bytes — and instead presents a Google-signed OIDC token
with the message id inside the body; a Wiz automation action sends only static headers, so it presents a shared bearer
token, its event type is inferred from the body's shape, and its delivery id is a digest of the body (Wiz sends no
GUID). Everything after authentication is the server's and identical for all of them.

## Context enhancers (`pkg/enhance`)

An enhancer adds organisational context to a freshly opened finding — ownership, tier, data classification, associated
infrastructure — before the investigation decides a route. Enhancers run as a chain in the context-controller; each
contributes an enrichment recorded on Finding status — semi-structured attributes (projected as `security-context`
tracking labels) and free-form markdown (projected as a sticky tracking comment, one per enhancer) — and a failing
enhancer logs and continues rather than blocking the pipeline.

An enhancer may also **resolve a repository** for a finding that arrived without one. That is the one enrichment written
to spec rather than status, and it is written **once**: three separate mechanisms snapshot a finding's repository
independently — the rollup ledger re-derives its scope key at reversal time, the investigation gate's clone artifact has
an immutable URL with no update path, and each agent Job records it in an annotation — so revising it later
desynchronises all three silently. The first enhancer in the chain to name one wins.

Repository resolution is the exception to "a failing enhancer logs and continues". The chain runs exactly once and there
is no transition back to `Opened`, so advancing a cloud finding after a failed lookup would lose its repository
permanently and hand it off unremediable. A failed lookup instead holds the finding at `Opened` and retries, bounded by
the accumulation window — past that it advances anyway, because a finding a human could be looking at is better than one
held out of sight.

Four implementations ship:

- **Static file** — a YAML map from repository to owners and attributes
  ([format](configuration/context-controller.md#the-static-context-enhancer)), standing in for a real CMDB.
- **Google Cloud labels** — reads `scm-repository-*` labels off the cloud resource a finding was raised against, via
  Cloud Asset Inventory, and resolves the repository from them
  ([format](integrations/google-cloud-scc.md#the-ownership-labels)). Configured on the `google-cloud` Integration's
  `cloudAssetInventory` block and read per enhancement, it acts on any Google Cloud finding whichever source ingested
  it, and stands aside when no Integration enables it.
- **AWS resource tags** — the same vocabulary spelled as tags, read from an organization-level inventory (an AWS Config
  aggregator or a Resource Explorer view), plus the resource's tags as attributes ([format](integrations/aws.md)).
  Configured on the `aws` Integration's `resourceTags` block, same rules: any AWS finding, whichever source, standing
  aside unless enabled.
- **Azure resource tags** — the same again for Azure, read from Azure Resource Graph (tenant-wide, no backend to
  choose), plus the resource's tags as attributes ([format](integrations/azure.md)). Configured on the `azure`
  Integration's `resourceTags` block, same rules: any Azure finding, whichever source, standing aside unless enabled.

A fifth chain entry is not one enhancer but a fan-out: the **generic HTTP enhancer** calls every `provider: generic`
Integration whose `enhance` capability is on ([contract](integrations/generic.md)), each under its own name, in name
order, after the cloud lookups and before the static file. It is how a real CMDB integrates without a rebuild.

A real CMDB integration implements the same interface — in-tree or over the generic contract: resolve the repository,
return owners and attributes, let the chain record them. The owners an enhancer reports are who patchy hands a finding
to when it routes to humans — the highest-leverage integration in the system.

## Harnesses and models

The agent stages are harness-agnostic by construction — the harness builds the CLI argv and parses its stdout, the
runner executes and enforces budgets. Today `claude` (Claude Code, Anthropic models), `codex` (the OpenAI Codex CLI,
OpenAI models) and `copilot` (the GitHub Copilot CLI, which brokers both vendors' models) are the built-in harnesses,
and `fake` replays recorded stream fixtures for tests and the dev overlay.

**Models are associated with harnesses, not chosen alongside them.** `internal/model` is a registry of canonical,
provider-qualified model ids (`anthropic/claude-sonnet-5`, `openai/gpt-5.3-codex`); each model records the harnesses
that can run it (with the CLI-specific model id each expects) and a preferred harness. Everything an operator or the
agent names a model with — the allowlist, the stage defaults, the investigation report's `model:` — is a canonical id.
The harness that runs a model is then _derived_: `harness.ResolveModel` picks the model's preferred harness when it is
enabled, so an OpenAI model routes to codex and an Anthropic model to claude. `copilot` is the reason a model records a
_set_ of harnesses rather than one: it can run every model in the registry, but it is no model's preferred harness, so
it only picks up work when a model's vendor-native harness is not enabled. Adding a model (or teaching an existing
harness a new one) is a registry edit; adding a whole new provider is a new harness plus its registry entries.

Each harness has its own **runner image** (`claude-agent-runner`, `codex-agent-runner`, `copilot-agent-runner`) bundling
just that CLI, its own credential Secret, and its own egress network policy. The remediation controller resolves the
investigation's chosen model to a harness _before_ launching, so the Job runs the matching image with the matching
credential — a claude pod reaching only `api.anthropic.com`, a codex pod only `api.openai.com`. Which harnesses a
deployment enables is configuration (`--harnesses`, defaulting to any whose credential Secret exists); startup fails
unless every allowlisted model has an enabled harness that can run it.

The `codex` harness runs `codex exec --json` with codex's own sandbox disabled — patchy confines the agent at the pod
layer (no network beyond the model API, no credentials), so the CLI's kernel sandbox is redundant there. Codex has no
equivalents for the tool allow/deny grammar or a turn ceiling; the wall-clock timeout and output-token budget are
enforced by the runner as usual, though codex reports usage only per completed turn, so the budget cannot fire mid-turn.
Codex reports token usage but not cost, so the rollup prices its tokens from the model registry's published rates.

The `copilot` harness runs `copilot -p --output-format json`, whose JSONL session-event stream carries per-model-call
usage — so unlike codex its budget _can_ fire mid-turn. It has no turn ceiling either, and its permission grammar
resolves deny over allow unconditionally, which makes the read-only posture inexpressible: a write rule broad enough to
stop source edits also stops the report the investigation exists to produce. URL access is denied in both postures and
the pod remains the real boundary. Copilot prices in premium requests rather than dollars, so like codex its runs are
priced from the registry's published rates.

Copilot is also the one harness whose credential is not a model API key: it authenticates with a **GitHub token**, the
credential class every other agent pod is built never to hold. The runner passes `--disable-builtin-mcps` so no tool in
the session can spend it against the GitHub API, `--no-remote`/`--no-remote-export` keep session content off GitHub's
web and mobile surfaces, and its egress policy admits only `api.github.com` (where the CLI exchanges the token) and the
`*.githubcopilot.com` inference endpoints. It ships disabled; enabling it is a deliberate trade of a broader credential
for one harness that can run every model.

## Ground rules

- `pkg/` signatures must not reference `internal/` types — the seams stay importable.
- Everything else is `internal/` and free to change between releases.
- The custom resources are the state, and the [projected labels](labels.md#the-projected-labels) are a one-way rendering
  of it: new sources and enhancers express state through the `Finding` schema, never by inventing parallel labels or
  parsing issues.
