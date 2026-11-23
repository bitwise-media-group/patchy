# Extending

Patchy ships GitHub code scanning, Google Cloud Security Command Center, Wiz and generic HTTP sources, and a chain of
cloud and HTTP context enhancers. Both ends of the pipeline are plugin interfaces. The public interfaces live under
`pkg/`. These are the only packages whose signatures are stable for external reuse:

| Package          | What it defines                                                                                                   |
| ---------------- | ----------------------------------------------------------------------------------------------------------------- |
| `pkg/source`     | `source.Handler` (ingest), and the optional `source.Resolver` (verdict write-back) and `source.Lister` (backfill) |
| `pkg/enhance`    | `enhance.Enhancer`, one context enhancer                                                                          |
| `pkg/generic`    | The generic integration's HTTP wire types, importable by an external process                                      |
| `pkg/evaluation` | The [remote evaluation](concepts/evaluation.md) contract evolve speaks; not a plugin interface                    |

The built-in implementations under `internal/ghas`, `internal/scc`, `internal/wiz` and `internal/enhancers` are
reference implementations of the same interfaces.

There are two ways to extend patchy:

- An **in-tree plugin** implements the Go interfaces below and is compiled into the controllers.
- An **external process** implements the [generic HTTP contract](integrations/sources/generic.md) instead. A
  `provider: generic` Integration calls it over signed HTTP for the same three extension points (source, write-back and
  enhancer), and you change no patchy code at all.

Use the generic contract first when you host the integration yourself. Use an in-tree plugin when the integration should
ship with patchy.

## Finding sources (`pkg/source`)

A source turns an external tool's alerts into patchy findings. It parses the delivery, fetches whatever detail the
tool's API offers, and hands the **integration-controller** a normalised finding. That finding contains:

- identifiers (CWE/CVE/GHSA), most authoritative first. The primary one keys accumulation.
- severity
- locations
- the evidence that becomes the Finding's description and the tracking issue's body

The built-in `ghas` handler does this for `code_scanning_alert` deliveries, and `ghas-secrets` for
`secret_scanning_alert` deliveries. `gcp-scc` does it for Security Command Center notifications, which need no API call
at all because the notification carries everything.

The interface is small:

```go
type Handler interface {
    ID() string // the source id, e.g. "ghas"; recorded as spec.source
    Findings(ctx context.Context, eventType string, payload []byte) ([]Finding, error)
}
```

The goal is that SAST tools, dependency scanners, cloud posture tools, or even agentic reviewers plug in here without
touching the accumulation, projection, or remediation code. The `Finding` schema does not depend on the source, and
`spec.source` (shown as the `security-source` label) records where a finding came from.

**Not every finding is about code.** A finding may name a repository, a `CloudResource`, or both, and that choice
decides how it accumulates: code findings group per repository, cloud findings per resource. A finding without a
repository is allowed. It goes through triage and is handed to a human instead of being remediated. It may also get a
repository later, from an enhancer. A finding that names neither is not allowed, because there would be nothing to
accumulate it against, so ingest rejects it.

**Some findings are leaked secrets.** A source sets `Finding.Category` to `source.CategorySecret` for a leaked
credential. Downstream branches on the category, never on the source id: the investigation gets the secret-triage
prompt, an `ignore` dismisses only above the confidence threshold, and a `remediate` always waits for human approval
because the real fix is rotation. Such a source must never put the secret value in any field. It locates the value
instead (`Location` carries columns as well as lines), and the agent pod scrubs the located literal from everything it
emits. A resolver receives the investigation's reason as one of the canonical `source.Reason*` values and maps it onto
its tool's vocabulary.

A source may also implement `source.Resolver` (`Resolve(ctx, alerts, verdict)`), the optional write-back. Write-back
tells the original tool what patchy decided, so a finding dismissed here does not stay open there. `ghas` implements it
by dismissing the code-scanning alert, and `ghas-secrets` by resolving the secret-scanning alert. The pipeline groups a
finding's alerts by the source recorded on each one and hands each source only its own alerts. So which source an alert
came from is recorded at ingest, never guessed from the shape of an identifier. If a source implements no write-back,
the pipeline skips that step. A source that only reads is still a complete source.

A source that can list its tool's open alerts may implement `source.Lister`
(`List(ctx, repos, yield) (complete bool, err error)`). This powers the
[manual backfill](integrations/sources/github.md#the-manual-backfill), which ingests alerts that arrived before webhooks
were set up. `complete` reports whether the walk covered everything or stopped at its budget.

### Adding a provider webhook

Each provider gets one `webhook.Endpoint` on the single internet-facing listener. The endpoint supplies the two things
that differ between providers:

- an `Authenticator`: how a delivery proves it is genuine
- a `Decoder`: where its event type and delivery id are found

The providers differ like this:

- **GitHub** signs an HMAC over the raw body and labels deliveries with headers.
- **Pub/Sub** push cannot compute an HMAC at all, because Pub/Sub builds the message and the sender never sees the
  bytes. Instead it presents a Google-signed OIDC token, and the message id is inside the body.
- **Wiz** automation actions send only static headers. So Wiz presents a shared bearer token, its event type is worked
  out from the body's shape, and its delivery id is a digest of the body (Wiz sends no GUID).

Everything after authentication is handled by the server and is the same for all of them.

## Context enhancers (`pkg/enhance`)

An enhancer implements `enhance.Enhancer`: an `ID()` and `Enhance(ctx, issue) (*Enrichment, error)`. It adds
organisational context to a newly opened finding (ownership, tier, data classification, associated infrastructure)
before the investigation decides a route. Enhancers run as a chain in the context-controller. Each one contributes an
enrichment recorded on Finding status, made of:

- semi-structured attributes, shown as `security-context` tracking labels
- free-form markdown, shown as a sticky tracking comment (one per enhancer)

A failing enhancer logs the error and the chain continues, so one failure does not block the pipeline.

An enhancer may also **resolve a repository** for a finding that arrived without one. This is the one enrichment written
to spec instead of status, and it is written **once**. Three separate mechanisms each take their own copy of a finding's
repository:

- The rollup ledger works out its scope key again when it reverses a count.
- The investigation gate's clone artifact has an immutable URL with no update path.
- Each agent Job records the repository in an annotation.

Changing the repository later would put all three out of sync without any error. The first enhancer in the chain to name
a repository wins.

Repository resolution is the exception to "a failing enhancer logs and continues". The chain runs only once, and there
is no transition back to `Opened`. If a cloud finding moved on after a failed lookup, it would lose its repository for
good and be handed off with no way to remediate it. So a failed lookup holds the finding at `Opened` and retries, up to
the end of the accumulation window. After that the finding moves on anyway, because a finding a human can see is better
than one held out of sight.

Four implementations ship:

- **Static file**: a YAML map from repository to owners and attributes
  ([format](configuration/context-controller.md#the-static-context-enhancer)). It stands in for a real CMDB.
- **Google Cloud Asset Inventory**: reads `scm-repository-*` labels off the cloud resource a finding was raised against,
  using Cloud Asset Inventory, and resolves the repository from them
  ([format](integrations/enhancers/index.md#the-ownership-labels)). You configure it in the `google-cloud` Integration's
  `cloudAssetInventory` block, which it reads on each enhancement. It acts on any Google Cloud finding, whichever source
  ingested it, and does nothing when no Integration enables it.
- **AWS resource tags**: the same vocabulary written as tags, read from an organization-level inventory (an AWS Config
  aggregator or a Resource Explorer view). It also records the resource's tags as attributes
  ([format](integrations/enhancers/aws.md)). You configure it in the `aws` Integration's `resourceTags` block. The same
  rules apply: it acts on any AWS finding, whichever source ingested it, and does nothing unless enabled.
- **Azure resource tags**: the same again for Azure, read from Azure Resource Graph (tenant-wide, no backend to choose).
  It also records the resource's tags as attributes ([format](integrations/enhancers/azure.md)). You configure it in the
  `azure` Integration's `resourceTags` block. The same rules apply: it acts on any Azure finding, whichever source
  ingested it, and does nothing unless enabled.

A fifth entry in the chain is a fan-out rather than a single enhancer. The **generic HTTP enhancer** calls every
`provider: generic` Integration whose `enhance` capability is on ([contract](integrations/enhancers/generic.md)). It
calls each one under its own name, in name order, after the cloud lookups and before the static file. This is how a real
CMDB connects without a rebuild.

A real CMDB integration implements the same interface, either in-tree or over the generic contract. It resolves the
repository and returns owners and attributes, and the chain records them. The owners an enhancer reports are the people
patchy hands a finding to when it routes to humans. That makes the CMDB integration the one with the most effect on how
findings reach people.

## Harnesses and models

A harness is the code that drives one coding-agent CLI. The agent stages work with any harness: the harness builds the
CLI argv and parses its stdout, and the runner executes it and enforces budgets. Today there are two built-in harnesses:

- `claude` (Claude Code, Anthropic models)
- `codex` (the OpenAI Codex CLI, OpenAI models)

Patchy supports only first-party coding agents, with each model running on its vendor's own CLI. A third harness,
`fake`, replays recorded stream fixtures for tests and the dev overlay.

**Each model is tied to the harnesses that can run it. You do not pick the harness separately.** `internal/model` is a
registry of canonical, provider-qualified model ids (`anthropic/claude-sonnet-5-5`, `openai/gpt-5.3-codex`). Each model
records the harnesses that can run it (with the CLI-specific model id each one expects) and a preferred harness.
Everywhere an operator or the agent names a model, it uses a canonical id: the allowlist, the stage defaults, and the
investigation report's `model:`. Patchy then _derives_ the harness that runs the model. `harness.ResolveModel` picks the
model's preferred harness when that harness is enabled, so an OpenAI model goes to codex and an Anthropic model to
claude. Every builtin model names only its vendor's harness. The registry still stores a set of harnesses, so a harness
whose CLI ids differ from the vendor's can be added without changing the type. Adding a model (or teaching an existing
harness a new one) is a registry edit. Adding a whole new provider needs a new harness plus its registry entries.

Ids resolve in three tiers (`model.Resolve`):

1. An exact registered id.
2. A **family alias** (`anthropic/claude-opus`), which resolves to the newest registered version of that family.
3. A **pattern** id: a well-formed `anthropic/claude-<family>-<major>[-<minor>]` with no registry entry. Patchy creates
   it as a model that runs only on the claude harness and has no price, so a new Anthropic release runs before it is
   registered.

The controllers canonicalize the allowlist and stage defaults once at startup (`model.Canonicalize`), so only concrete
ids reach pods, CRDs and rollups. Registering a new model therefore also moves every alias that names its family. Keep
superseded entries, because stored findings and rollups still need their pricing. Every Anthropic builtin's bare id must
parse as `claude-<family>-<major>[-<minor>]` (the registry tests enforce this), or its alias cannot sort it. The shipped
defaults are the alias `anthropic/claude-sonnet` for both stages and `anthropic/claude-sonnet,anthropic/claude-opus` for
the allowlist. For the operator view, see [Models and credentials](deployment/models.md).

Each harness has its own **runner image** (`claude-agent-runner`, `codex-agent-runner`) that bundles only that CLI, and
its own egress network policy. The remediation controller resolves the investigation's chosen model to a harness
_before_ it launches the Job, so the Job runs the matching image under the matching policy. Claude and codex pods both
hold no credential and reach only hosts inside the cluster. Their model traffic goes through the egress broker, which
adds the credential on the way out. Claude reaches the broker through its CLI's base-URL env, and codex through a
rendered `config.toml` model provider. Which harnesses a deployment enables is configuration: `--harnesses`, or every
harness whose runner image is set. Startup fails unless every allowlisted model has an enabled harness that can run it.

The `codex` harness runs `codex exec --json` with codex's own sandbox turned off. Patchy confines the agent at the pod
level (no network beyond the egress broker, no credentials), so the CLI's kernel sandbox adds nothing there. Codex has
no equivalent of the tool allow/deny grammar or a turn limit. The runner still enforces the wall-clock timeout and the
output-token budget. Codex reports usage only when a turn completes, though, so the budget cannot stop it partway
through a turn. Codex reports token usage but not cost, so the rollup prices its tokens from the model registry's
published rates.

## Ground rules

- `pkg/` signatures must not reference `internal/` types, so the plugin interfaces stay importable.
- Everything else is `internal/` and can change between releases.
- The custom resources are the state, and the [projected labels](concepts/issue-labels.md) are a one-way rendering of
  it. New sources and enhancers express state through the `Finding` schema. They never invent parallel labels or parse
  issues.

Contributors changing the `Finding` schema itself should read
[Adding a field to FindingSpec](contributing/finding-fields.md) first.
