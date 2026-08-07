# Integrations

Everything patchy knows about the outside world enters and leaves through two kinds of custom resource, and the
separation is deliberate:

| Resource      | Direction                     | Used by                                                  | Holds                                |
| ------------- | ----------------------------- | -------------------------------------------------------- | ------------------------------------ |
| `Integration` | events, issues and enrichment | integration-controller, context-controller               | a webhook secret and/or a credential |
| `Forge`       | repository access             | source-controller (read), remediation-controller (write) | a credential                         |

An `Integration` is the events plane: findings in, tracking issues and verdicts out, context looked up. A `Forge`
answers one question — _how do I clone and push this repository?_ — and is the only place a repository credential is
exercised. They are separate objects because they are separate blast radii: one provider account (a single GitHub App,
today) can back both, and splitting read and write across two credentials is a supported posture rather than a refactor.

An `Integration` plays up to three roles, and this section documents each role on its own page:

- A **finding source** ingests an external tool's alerts as findings. A source may also write the triage verdict back —
  dismissing the alert, rejecting the issue — so a finding dismissed here does not stay open there.
- A **context enhancer** adds organisational context to freshly opened findings — ownership, attributes, and for cloud
  findings the [owning repository](enhancers/index.md).
- The **tracking projection** (`github.issues`) renders findings as human-facing issues, whichever source they came
  from.

## Providers and their roles

| Provider     | Finding source                        | Verdict write-back                                              | Context enhancer                                 | Forge                   | Tracking issues          |
| ------------ | ------------------------------------- | --------------------------------------------------------------- | ------------------------------------------------ | ----------------------- | ------------------------ |
| GitHub       | [code scanning](sources/github.md)    | [alert dismissal](sources/github.md#what-patchy-writes-back)    | —                                                | [yes](forges/github.md) | [yes](sources/github.md) |
| Google Cloud | [SCC](sources/google-scc.md)          | [mute — not yet](sources/google-scc.md#what-is-not-implemented) | [Cloud Asset Inventory](enhancers/google-cai.md) | —                       | —                        |
| Wiz          | [Issues + Defend](sources/wiz.md)     | [issue rejection](sources/wiz.md#write-back-optional)           | —                                                | —                       | —                        |
| AWS          | —                                     | —                                                               | [resource tags](enhancers/aws.md)                | —                       | —                        |
| Azure        | —                                     | —                                                               | [resource tags](enhancers/azure.md)              | —                       | —                        |
| Generic HTTP | [inbound webhook](sources/generic.md) | [resolver](sources/generic.md#outbound-the-resolver-call)       | [enhance](enhancers/generic.md)                  | —                       | —                        |

A provider spanning several columns is still **one Integration** — the `google-cloud` resource carries both the SCC
source and the Cloud Asset Inventory enhancer as independently enabled capabilities, and a `generic` resource can be a
source, an enhancer, or both. The split pages cross-reference each other, and each capability is configured on the one
shared resource.

Every provider is a namespace singleton except generic: define as many `provider: generic` Integrations as you have
external processes, each under its own name. A finding records where it came from as its source id — `ghas`, `gcp-scc`,
`wiz-issues`, `wiz-defend`, or a generic Integration's own name — projected as the `security-source` tracking label.

Sources without their own tracking surface lean on a `github` Integration with `issues` enabled: findings from SCC, Wiz
or a generic source get their tracking issues through it, in the repository each finding resolved to (or the
[fallback repository](sources/github.md#the-fallback-repository) when none did).

Building an integration of your own — in-tree or over the generic HTTP contract — is covered in
[Extending](../extending.md).
