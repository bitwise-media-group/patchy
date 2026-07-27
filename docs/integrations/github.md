# GitHub

GitHub is the only **two-way** integration: findings come in from code scanning, and patchy writes back — tracking
issues, dismissals, remediation branches and pull requests. Everything else in the system is one-way.

That split lives in two custom resources, and the separation is deliberate:

| Resource      | Direction         | Used by                                                  | Holds                             |
| ------------- | ----------------- | -------------------------------------------------------- | --------------------------------- |
| `Integration` | events and issues | integration-controller                                   | the webhook secret + a credential |
| `Forge`       | repository access | source-controller (read), remediation-controller (write) | a credential                      |

One GitHub App can back both. They are separate objects because they are separate blast radii — the `Forge` is the only
place a write credential is exercised, and splitting read and write across two Apps is a supported posture rather than a
refactor.

If you have not registered the App yet, start at [Create the GitHub App](../getting-started/github-app.md); this page is
about what the resulting resources do.

## How alerts arrive

`POST /github/webhooks` is the one URL the App delivers to. Each delivery's `X-Hub-Signature-256` is HMAC-validated
against the `webhookSecret` of every configured `Integration`, before any handling — see
[integration-controller](../configuration/integration-controller.md#the-webhook-receiver) for the response codes and the
delivery-queue behaviour.

```yaml
apiVersion: patchy.bitwisemedia.uk/v1alpha1
kind: Integration
metadata:
  name: github
  namespace: patchy
spec:
  provider: github
  secretRef:
    name: patchy-github # appID + privateKey (or token), plus webhookSecret
  interval: 10m # credential revalidation
  github:
    # baseURL: https://ghes.example.com/api/v3   # GHES only
    codeScanningAlerts:
      enabled: true # ingestion, and dismissal on an "ignore" verdict
    issues:
      enabled: true # the tracking projection and the human signals back
      approveComment: /approve
```

Each capability block is independent. An `Integration` with only `codeScanningAlerts` ingests findings and never opens
an issue; one with only `issues` is a tracking surface for findings that arrived from somewhere else — which is exactly
how a [Google Cloud SCC](google-cloud-scc.md) estate gets its tracking issues.

Only `created` and `reopened` alert actions produce a finding. `fixed`, `closed_by_user`, `appeared_in_branch` and the
rest are state GitHub already manages.

## What patchy keeps

Unlike an SCC notification, the webhook payload is only a summary — the rule help markdown comes from the API — so
ingest fetches the full alert before building the `Finding`:

| Finding field             | From                                                                      |
| ------------------------- | ------------------------------------------------------------------------- |
| `spec.repository`         | the delivery's repository owner and name                                  |
| `spec.advisories`         | GHSA ids, then CVEs, then CWEs from the CodeQL rule tags                  |
| `spec.alerts[].id`        | the alert number; `.url` is its HTML URL                                  |
| `spec.alerts[].locations` | the alert's path, line range and snippet                                  |
| `spec.severity`           | `security_severity_level`, or the raw rule severity mapped onto the scale |
| `spec.description`        | the rule description and help text                                        |

Advisories are ordered most-authoritative-first, and a rule with no recognizable advisory falls back to `rule:<rule id>`
so accumulation still has a stable key. Alerts sharing an advisory family against the same repository fold into one
`Finding` until the accumulation window closes.

## What patchy writes back

Three write paths, each gated differently:

- **The tracking issue.** A Finding is projected to an issue: templated body, the
  [projected labels](../labels.md#the-projected-labels), enrichments and reports as comments, and open/closed state.
  Strictly one-way — issue state is never parsed back into pipeline state, only the explicit signals below are.
- **Alert dismissal.** An `ignore` verdict closes the code-scanning alert as a false positive. This is the write-back
  half of the `codeScanningAlerts` capability; the SCC analogue (muting) is
  [not yet implemented](google-cloud-scc.md#what-is-not-implemented).
- **The remediation branch and pull request.** Pushed through the `Forge`, not the `Integration` — a different
  credential and a different controller.

### Human signals

These are the only things on GitHub that move `Finding` state:

| Signal                                   | Effect                      |
| ---------------------------------------- | --------------------------- |
| Issue closed                             | → `HandedOff`               |
| Issue reopened                           | `Dismissed` → `HandedOff`   |
| `/approve` comment                       | recorded on `spec.approval` |
| PR on `patchy/<finding>` merged          | `InReview` → `Remediated`   |
| PR on `patchy/<finding>` closed unmerged | → `Failed`                  |

`approveComment` changes the command; it defaults to `/approve`. Who may approve is RBAC on the status page and the CLI,
but on an issue it is whoever can comment — so treat the comment as a convenience for repositories whose write access
already matches your approval policy.

## Repository access

A `Forge` answers "how do I clone and push this repository". It is matched by host equality, then the optional `orgs`
allowlist, then the optional repository-name regexes; **the most-constrained match wins**, so a narrow `Forge` for one
sensitive org overrides a broad default without ordering rules.

```yaml
apiVersion: patchy.bitwisemedia.uk/v1alpha1
kind: Forge
metadata:
  name: github
  namespace: patchy
spec:
  provider: github
  secretRef:
    name: patchy-github
  # orgs: [acme]                          # optional allowlist
  # repositories: ["^acme/payments-.*$"]  # optional regexes
  interval: 10m
```

Agent pods never hold this credential, or any other. source-controller downloads the archive at a pinned SHA and serves
it from its own artifact endpoint; remediation-controller replays the agent's changeset through the Git Data API. See
[the isolation model](../deployment/isolation.md).

## Credentials

One Secret, referenced by both resources. Two accepted shapes:

| Keys                   | Use                                                             |
| ---------------------- | --------------------------------------------------------------- |
| `appID` + `privateKey` | GitHub App — short-lived, single-repository installation tokens |
| `token`                | A personal access token, for development                        |

Plus `webhookSecret` on the `Integration`'s Secret, for receiver HMAC validation.

Prefer the App. Installation tokens are minted per repository per operation and expire in an hour; a PAT is long-lived,
carries its owner's whole access, and cannot use the delivery sweep below. Each resource revalidates its Secret on
`spec.interval` and reports the result on its `Ready` condition, so a rotated-away credential surfaces as a condition
rather than as a failed run.

## The failed-delivery sweep

GitHub does not retry a failed webhook delivery. Anything missed while the receiver was down, or while its queue was
full, sits in the 30-day delivery log until someone redelivers it by hand.

```yaml
spec:
  github:
    redelivery:
      enabled: true
      lookback: 24h # bounded by GitHub's 30-day retention
```

On each reconcile interval the controller lists recent deliveries and asks GitHub to redeliver those that never got a
2xx. **App credentials are required** — the delivery log is App-scoped, so a PAT integration cannot sweep.

## GitHub Enterprise Server

Point both resources at your instance and everything else is identical:

```yaml
# Integration
spec:
  github:
    baseURL: https://ghes.example.com/api/v3

# Forge
spec:
  baseURL: https://ghes.example.com/api/v3
```
