# Extending

Patchy ships GHAS/CodeQL support and a placeholder context enhancer, but both ends of the pipeline are plugin seams. The
public interfaces live under `pkg/` — the only packages whose signatures are stable for external reuse — and the
built-in implementations under `internal/ghas` and `internal/enhancers` are reference implementations of the same
interfaces.

## Finding sources (`pkg/source`)

A source turns an external tool's alerts into patchy findings: it parses the webhook payload, fetches whatever detail
the tool's API offers, and hands the source-controller a normalised finding — identifiers (CWE/CVE/GHSA), severity,
locations, and the evidence that becomes the issue's machine manifest. The built-in `ghas` source does exactly this for
`code_scanning_alert` deliveries.

The design intent (see `DESIGN.md`) is that SAST tools, dependency scanners, or even agentic reviewers plug in here
without touching the accumulation, labeling, or remediation machinery — the issue's label taxonomy is source-agnostic,
and `security-source` records where a finding came from.

## Context enhancers (`pkg/enhance`)

An enhancer adds organisational context to a freshly opened finding — ownership, tier, data classification, associated
infrastructure — before the classifier decides a route. Enhancers run as a chain in the context-controller; each
contributes to the enrichment comment, and a failing enhancer logs and continues rather than blocking the pipeline.

Two implementations ship:

- **Noop** — the default when nothing is configured.
- **Static file** — a YAML map from repository to owners and attributes
  ([format](configuration/context-controller.md#the-static-context-enhancer)), standing in for a real CMDB.

A real CMDB integration implements the same interface: resolve the repository, return owners and attributes, let the
chain render them. The owners an enhancer reports are who patchy assigns when a finding routes to humans — the
highest-leverage integration in the system.

## Harnesses

The agent stages are harness-agnostic by construction — the harness builds the CLI argv and parses its stdout, the
runner executes and enforces budgets, and the two stages are configured independently (`--classify-harness` /
`--remediate-harness`). Today `claude` is the production harness and `fake` replays recorded stream fixtures for tests
and the dev overlay; the seam exists so classification and remediation could run on different agents without touching
the controllers.

## Ground rules

- `pkg/` signatures must not reference `internal/` types — the seams stay importable.
- Everything else is `internal/` and free to change between releases.
- The label taxonomy is the public contract on the GitHub side: new sources and enhancers must express state through
  [the existing labels](labels.md), never invent parallel ones.
