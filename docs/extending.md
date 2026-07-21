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
contributes an enrichment recorded on Finding status (and projected as a tracking-issue comment), and a failing enhancer
logs and continues rather than blocking the pipeline.

Two implementations ship:

- **Noop** — the default when nothing is configured.
- **Static file** — a YAML map from repository to owners and attributes
  ([format](configuration/context-controller.md#the-static-context-enhancer)), standing in for a real CMDB.

A real CMDB integration implements the same interface: resolve the repository, return owners and attributes, let the
chain record them. The owners an enhancer reports are who patchy hands a finding to when it routes to humans — the
highest-leverage integration in the system.

## Harnesses

The agent stages are harness-agnostic by construction — the harness builds the CLI argv and parses its stdout, the
runner executes and enforces budgets, and the two stages are configured independently (`--investigate-harness` on the
[investigation-controller](configuration/investigation-controller.md), `--remediate-harness` on the
[remediation-controller](configuration/remediation-controller.md)). Today `claude` is the production harness and `fake`
replays recorded stream fixtures for tests and the dev overlay; the seam exists so investigation and remediation could
run on different agents without touching the controllers.

## Ground rules

- `pkg/` signatures must not reference `internal/` types — the seams stay importable.
- Everything else is `internal/` and free to change between releases.
- The custom resources are the state, and the [projected labels](labels.md#the-projected-labels) are a one-way rendering
  of it: new sources and enhancers express state through the `Finding` schema, never by inventing parallel labels or
  parsing issues.
