# Extending

Patchy ships GHAS/CodeQL support and a placeholder context enhancer, but both ends of the pipeline are plugin seams. The
public interfaces live under `pkg/` — the only packages whose signatures are stable for external reuse — and the
built-in implementations under `internal/ghas` and `internal/enhancers` are reference implementations of the same
interfaces.

## Finding sources (`pkg/source`)

A source turns an external tool's alerts into patchy findings: it parses the webhook payload, fetches whatever detail
the tool's API offers, and hands the **integration-controller** a normalised finding — identifiers (CWE/CVE/GHSA, most
authoritative first; the primary one keys accumulation), severity, locations, and the evidence that becomes the
Finding's description and the tracking issue's body. The built-in `ghas` handler does exactly this for
`code_scanning_alert` deliveries.

The design intent (see `DESIGN.md`) is that SAST tools, dependency scanners, or even agentic reviewers plug in here
without touching the accumulation, projection, or remediation machinery — the `Finding` schema is source-agnostic, and
`spec.source` (projected as the `security-source` label) records where a finding came from. A finding without an
identifiable repository is legal too: it flows through triage but can never reach the remediation phases.

## Context enhancers (`pkg/enhance`)

An enhancer adds organisational context to a freshly opened finding — ownership, tier, data classification, associated
infrastructure — before the investigation decides a route. Enhancers run as a chain in the context-controller; each
contributes an enrichment recorded on Finding status — semi-structured attributes (projected as `security-context`
tracking labels) and free-form markdown (projected as a sticky tracking comment, one per enhancer) — and a failing
enhancer logs and continues rather than blocking the pipeline.

Two implementations ship:

- **Noop** — the default when nothing is configured.
- **Static file** — a YAML map from repository to owners and attributes
  ([format](configuration/context-controller.md#the-static-context-enhancer)), standing in for a real CMDB.

A real CMDB integration implements the same interface: resolve the repository, return owners and attributes, let the
chain record them. The owners an enhancer reports are who patchy hands a finding to when it routes to humans — the
highest-leverage integration in the system.

## Harnesses and models

The agent stages are harness-agnostic by construction — the harness builds the CLI argv and parses its stdout, the
runner executes and enforces budgets. Today `claude` (Claude Code, Anthropic models) and `codex` (the OpenAI Codex CLI,
OpenAI models) are the built-in harnesses, and `fake` replays recorded stream fixtures for tests and the dev overlay.

**Models are associated with harnesses, not chosen alongside them.** `internal/model` is a registry of canonical,
provider-qualified model ids (`anthropic/claude-sonnet-5`, `openai/gpt-5.3-codex`); each model records the harnesses
that can run it (with the CLI-specific model id each expects) and a preferred harness. Everything an operator or the
agent names a model with — the allowlist, the stage defaults, the investigation report's `model:` — is a canonical id.
The harness that runs a model is then _derived_: `harness.ResolveModel` picks the model's preferred harness when it is
enabled, so an OpenAI model routes to codex and an Anthropic model to claude. Adding a model (or teaching an existing
harness a new one) is a registry edit; adding a whole new provider is a new harness plus its registry entries.

Each harness has its own **runner image** (`claude-agent-runner`, `codex-agent-runner`) bundling just that CLI, its own
credential Secret, and its own egress network policy. The remediation controller resolves the investigation's chosen
model to a harness _before_ launching, so the Job runs the matching image with the matching credential — a claude pod
reaching only `api.anthropic.com`, a codex pod only `api.openai.com`. Which harnesses a deployment enables is
configuration (`--harnesses`, defaulting to any whose credential Secret exists); startup fails unless every allowlisted
model has an enabled harness that can run it.

The `codex` harness runs `codex exec --json` with codex's own sandbox disabled — patchy confines the agent at the pod
layer (no network beyond the model API, no credentials), so the CLI's kernel sandbox is redundant there. Codex has no
equivalents for the tool allow/deny grammar or a turn ceiling; the wall-clock timeout and output-token budget are
enforced by the runner as usual, though codex reports usage only per completed turn, so the budget cannot fire mid-turn.
Codex reports token usage but not cost, so the rollup prices its tokens from the model registry's published rates.

## Ground rules

- `pkg/` signatures must not reference `internal/` types — the seams stay importable.
- Everything else is `internal/` and free to change between releases.
- The custom resources are the state, and the [projected labels](labels.md#the-projected-labels) are a one-way rendering
  of it: new sources and enhancers express state through the `Finding` schema, never by inventing parallel labels or
  parsing issues.
