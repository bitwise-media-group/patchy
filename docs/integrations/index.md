# Integrations

Everything patchy knows about the outside world enters and leaves through two kinds of custom resource, kept separate on
purpose:

| Resource      | Direction                     | Used by                                                  | Holds                                |
| ------------- | ----------------------------- | -------------------------------------------------------- | ------------------------------------ |
| `Integration` | events, issues and enrichment | integration-controller, context-controller               | a webhook secret and/or a credential |
| `Forge`       | repository access             | source-controller (read), remediation-controller (write) | a credential                         |

An `Integration` is the events plane: findings come in, tracking issues and verdicts go out, and context is looked up. A
`Forge` answers one question: _how do I clone and push this repository?_ It is the only place a repository credential is
used. They are separate objects so that a leaked or misused credential affects less. One provider account (a single
GitHub App, today) can back both. You can also split read and write across two credentials, and this is supported
without code changes.

An `Integration` plays up to three roles, and this section documents each role on its own page:

- A **finding source** ingests an external tool's alerts as findings. A source can also write the triage verdict back
  (dismissing the alert, rejecting the issue), so a finding dismissed in patchy does not stay open in the source tool.
- A **context enhancer** adds organisational context to newly opened findings: ownership, attributes, and for cloud
  findings the [owning repository](enhancers/index.md).
- The **tracking projection** (`github.issues`) renders findings as human-facing issues, whichever source they came
  from.

## Providers and their roles

| Provider     | Finding source                        | Verdict write-back                                              | Context enhancer                                        | Forge                   | Tracking issues          |
| ------------ | ------------------------------------- | --------------------------------------------------------------- | ------------------------------------------------------- | ----------------------- | ------------------------ |
| GitHub       | [code scanning](sources/github.md)    | [alert dismissal](sources/github.md#what-patchy-writes-back)    | no                                                      | [yes](forges/github.md) | [yes](sources/github.md) |
| Google Cloud | [SCC](sources/google-scc.md)          | [mute (not yet)](sources/google-scc.md#what-is-not-implemented) | [Google Cloud Asset Inventory](enhancers/google-cai.md) | no                      | no                       |
| Wiz          | [Issues + Defend](sources/wiz.md)     | [issue rejection](sources/wiz.md#write-back-optional)           | no                                                      | no                      | no                       |
| AWS          | no                                    | no                                                              | [resource tags](enhancers/aws.md)                       | no                      | no                       |
| Azure        | no                                    | no                                                              | [resource tags](enhancers/azure.md)                     | no                      | no                       |
| Generic HTTP | [inbound webhook](sources/generic.md) | [resolver](sources/generic.md#outbound-the-resolver-call)       | [enhance](enhancers/generic.md)                         | no                      | no                       |

A provider that spans several columns is still **one Integration**. The `google-cloud` resource carries both the SCC
source and the Google Cloud Asset Inventory enhancer, and you enable each one separately. A `generic` resource can be a
source, an enhancer, or both. The pages for each role link to each other, and you configure every capability on the one
shared resource.

Every provider is a namespace singleton except generic. You can define one `provider: generic` Integration for each
external process, each under its own name. A finding records where it came from as its source id: `ghas`, `gcp-scc`,
`wiz-issues`, `wiz-defend`, or a generic Integration's own name. Patchy shows this as the `security-source` tracking
label.

Sources without their own tracking surface rely on a `github` Integration with `issues` enabled. Findings from SCC, Wiz
or a generic source get their tracking issues through it. The issue goes in the repository each finding resolved to, or
in the [fallback repository](sources/github.md#the-fallback-repository) when none was found.

## Suspending an Integration or a Forge

Both kinds take `spec.suspend: true`. This takes the resource out of service without deleting it or its Secret:

A suspended **Integration** is skipped everywhere:

- The receiver stops matching deliveries against its secret, so its provider's webhooks are rejected.
- Its enhancer is skipped in the chain.
- Its reconcile loop (credential checks, the redelivery sweep, backfill) stops.
- It no longer offers a tracking projection.

A suspended **Forge** stops being validated and drops out of repository resolution. Findings it would have covered wait
with `ForgeResolved: False` until you resume it or another Forge covers them.

```sh
kubectl -n patchy patch integration github --type merge -p '{"spec":{"suspend":true}}'
```

[Extending](../extending.md) covers building your own integration, either in-tree or over the generic HTTP contract.
