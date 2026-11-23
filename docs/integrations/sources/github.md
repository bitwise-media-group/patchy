# GitHub code and secret scanning

GitHub is the only **two-way** source. Findings come in from code scanning and secret scanning, and patchy writes back
to GitHub: tracking issues, dismissals, remediation branches and pull requests. Every other source is one-way.

!!! info "One provider, two resources"

    This page covers the GitHub **Integration**: alerts come in, and tracking issues and dismissals go out. Cloning
    and pushing to repositories is the job of the separate [GitHub **Forge**](../forges/github.md). One GitHub App can
    back both. The [integrations overview](../index.md) maps the roles of every provider.

If you have not registered the App yet, start at [Create the GitHub App](../../getting-started/github-app.md). This page
covers what the resulting resource does.

## How alerts arrive

`POST /github/webhooks` is the only URL the App delivers to. Before any handling, the receiver checks each delivery's
`X-Hub-Signature-256` HMAC signature against the `webhookSecret` of every configured `Integration`. See
[integration-controller](../../configuration/integration-controller.md#the-webhook-receiver) for the response codes and
the delivery-queue behaviour.

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
    secretScanningAlerts:
      enabled: true # leaked credentials; see "Secret scanning" below
    issues:
      enabled: true # the tracking projection and the human signals back
      approveComment: /approve
```

Each capability block is independent. An `Integration` with only `codeScanningAlerts` ingests findings and never opens
an issue. One with only `issues` tracks findings that arrived from another source. That is how a
[Google Cloud SCC](google-scc.md) estate gets its tracking issues.

Only the `created` and `reopened` alert actions produce a finding. GitHub already manages the state behind `fixed`,
`closed_by_user`, `appeared_in_branch` and the other actions.

## What patchy keeps

Unlike an SCC notification, the webhook payload is only a summary (the rule help markdown comes from the API). So ingest
fetches the full alert before it builds the `Finding`:

| Finding field             | From                                                                      |
| ------------------------- | ------------------------------------------------------------------------- |
| `spec.repository`         | the delivery's repository owner and name                                  |
| `spec.advisories`         | GHSA ids, then CVEs, then CWEs from the CodeQL rule tags                  |
| `spec.alerts[].id`        | the alert number; `.url` is its HTML URL                                  |
| `spec.alerts[].locations` | the alert's path, line range and snippet                                  |
| `spec.severity`           | `security_severity_level`, or the raw rule severity mapped onto the scale |
| `spec.description`        | the rule description and help text                                        |

Advisories are ordered with the most authoritative first. A rule with no recognizable advisory falls back to
`rule:<rule id>`, so accumulation still has a stable key. Alerts in the same advisory family against the same repository
fold into one `Finding` until the accumulation window closes.

## Secret scanning

`secretScanningAlerts` ingests `secret_scanning_alert` deliveries as findings from the `ghas-secrets` source. A leaked
credential is not a code flaw. The real fix is rotating or revoking it with its issuer, and patchy cannot do that. So
these findings take a more cautious path through the pipeline than code-scanning ones.

**The secret value never enters patchy.** The delivery carries the plaintext, so the receiver reads only the action, the
alert number and the repository from it. It then fetches the alert and its locations again with `hide_secret=true`, and
the client has no field to keep a value in even if one came back. The value is not in the `Finding`, the tracking issue,
the prompts or the logs. The agent pod does hold the repository, so it can read the secret. The Job therefore carries
the secret's locations (path, lines and columns), and agent-runner slices the literal out of the working tree and scrubs
it from the transcript, the report and the envelope.

| Finding field             | From                                                                                             |
| ------------------------- | ------------------------------------------------------------------------------------------------ |
| `spec.source`             | `ghas-secrets`                                                                                   |
| `spec.category`           | `secret`                                                                                         |
| `spec.advisories`         | `secret:<secret_type>`, e.g. `secret:github_personal_access_token`                               |
| `spec.title`              | `<secret type display name> exposed`                                                             |
| `spec.alerts[].locations` | each commit location's path, lines and columns (other kinds are listed in the description)       |
| `spec.severity`           | `critical` if the credential is `active` or publicly leaked, `medium` if `inactive`, else `high` |
| `spec.description`        | type, provider, validity, the GitHub flags, every location, and rotation-first guidance          |

Each leaked instance is its own alert. Alerts of the same secret type in one repository accumulate into one finding.

How a secret finding is routed after the investigation:

| Verdict                             | Next phase         | What happens                                                          |
| ----------------------------------- | ------------------ | --------------------------------------------------------------------- |
| `ignore`, at or above the threshold | `Dismissed`        | The alert is **resolved** on GitHub with the agent's `dismiss_reason` |
| `ignore`, below the threshold       | `HandedOff`        | A human decides; the alert stays open                                 |
| `remediate`                         | `AwaitingApproval` | Always held (`secretRotation`), however confident the investigation   |
| `manual`                            | `HandedOff`        | A human handles it                                                    |

The investigation decides whether the value is a live credential, a placeholder or example, or a test fixture, whether
it is still at HEAD, and what it grants. On an `ignore` it sets `dismiss_reason` to `false_positive`, `used_in_tests` or
`wont_fix`, which becomes the GitHub resolution. The confidence threshold is the same `--confidence-threshold` the
remediation gate uses.

An approved `remediate` opens a **cleanup pull request**. It replaces the literal with a configuration or secret-store
reference, never rewrites history, and opens with a "Rotate this credential before merging" warning. Approve it only
once the credential has been rotated. The removed line does appear in the change itself and in the run archive's patch.
That is the same content the repository and its history already hold.

Only the `created` and `reopened` actions produce a finding. `resolved`, `validated`, `publicly_leaked` and the other
actions change state on an alert patchy already has, and are not acted on yet: a secret that becomes publicly leaked
after ingest keeps the severity it arrived with. A `category` field in the [generic source contract](generic.md), so
that other tools can raise secret findings, is also not there yet.

## What patchy writes back

Patchy writes back in three ways, and each one is gated differently:

- **The tracking issue.** A Finding is projected to an issue. The issue carries a templated body, the
  [projected labels](../../concepts/issue-labels.md), enrichments and reports as comments, and open/closed state. This
  is strictly one-way. Patchy never parses issue state back into pipeline state. Only the explicit signals below count.
- **Alert dismissal.** An `ignore` verdict closes the code-scanning alert as a false positive. This is the write-back
  half of the `codeScanningAlerts` capability. A secret-scanning alert is resolved instead, with the investigation's
  reason, and only above the confidence threshold. The SCC equivalent (muting) is
  [not yet implemented](google-scc.md#what-is-not-implemented).
- **The remediation branch and pull request.** These are pushed through the [`Forge`](../forges/github.md) rather than
  the `Integration`, with a different credential and a different controller.

### The fallback repository

A cloud finding that [never resolves a repository](../enhancers/index.md#when-no-repository-resolves) has no obvious
place for its tracking issue. `github.issues.fallbackRepository` (`"owner/repo"`) names one. Findings with no repository
of their own are projected there instead, so humans can still see them. They still cannot be remediated: the issue gives
humans visibility, but there is no code for an agent to change.

### Human signals

These are the only things on GitHub that change `Finding` state:

| Signal                                   | Effect                      |
| ---------------------------------------- | --------------------------- |
| Issue closed                             | → `HandedOff`               |
| Issue reopened                           | `Dismissed` → `HandedOff`   |
| `/approve` comment                       | recorded on `spec.approval` |
| PR on `patchy/<finding>` merged          | `InReview` → `Remediated`   |
| PR on `patchy/<finding>` closed unmerged | → `Failed`                  |

`approveComment` changes the command. It defaults to `/approve`. On the status page and the CLI, RBAC decides who may
approve. On an issue, anyone who can comment can approve. So use the comment only as a convenience, in repositories
whose write access already matches your approval policy.

## Credentials

The Integration uses one Secret, which the [Forge](../forges/github.md#credentials) can reference too. It accepts two
shapes:

| Keys                   | Use                                                            |
| ---------------------- | -------------------------------------------------------------- |
| `appID` + `privateKey` | GitHub App: short-lived, single-repository installation tokens |
| `token`                | A personal access token, for development                       |

The `Integration`'s Secret also holds `webhookSecret`, which the receiver uses for HMAC validation.

Prefer the App. Installation tokens are minted per repository for each operation, and they expire in an hour. A PAT is
long-lived, carries all of its owner's access, and cannot use the delivery sweep below. Each resource revalidates its
Secret every `spec.interval` and reports the result on its `Ready` condition. So a credential that was rotated away
shows up as a condition instead of as a failed run.

## The failed-delivery sweep

GitHub does not retry a failed webhook delivery. A delivery can be missed while the receiver is down, while its queue is
full, or while its secret is wrong. The missed delivery then sits in the App's 30-day delivery log until someone
redelivers it by hand. When you enable the sweep on the Integration, it does that for you:

```yaml
spec:
  interval: 10m # the sweep runs on the reconcile interval
  github:
    redelivery:
      enabled: true
      lookback: 24h # how far back each sweep scans (GitHub retains 30d)
```

Each sweep lists the App webhook's recent deliveries. It asks GitHub to redeliver every delivery whose attempts within
the lookback all failed. It stops once a delivery shows three failed attempts in the window, counting the original
delivery, so a delivery that always fails is retried about once per lookback period instead of on every sweep. **App
credentials are required**: the delivery log belongs to the App, so a PAT integration cannot sweep. Duplicates do no
harm, because the receiver deduplicates by delivery GUID and ingestion is idempotent (ingesting the same alert twice has
the same effect as once). The outcome lands on `status.redelivery` (scan size, redeliveries requested, truncation, last
error).

### Replaying every delivery

The status page's user menu offers **Replay deliveries** (RBAC verb `replay` on integrations). It writes `spec.replay`
on the Integration. On its next reconcile, the controller redelivers _everything_ in the lookback window, including
deliveries that already succeeded. Together with **Reset all data** (verb `reset`), it re-runs the pipeline from
ingestion, which is what a demo needs. See [Demo mode](../../deployment/dev-fake.md#the-demo-loop-replay-and-reset) for
what Reset deletes.

## The manual backfill

The sweep can only re-send deliveries that happened. Alerts raised _before_ the App was installed on (or associated
with) a repository never produced a delivery, so no replay finds them. The backfill finds them. It is a one-shot walk of
the provider's **open** alerts through the list API: code-scanning alerts, secret-scanning alerts, or both, by which
capabilities the Integration enables. It ingests each alert in the same way a webhook delivery would. This is
idempotent, so alerts that already have findings fold into them.

Request it by setting `spec.backfill`. You can do this from the status page's configuration view, with
`patchy backfill <integration>`, or directly. The RBAC verb is `backfill` on `integrations`, and the
[admission policy](../../deployment/kustomize.md) enforces it for every client. The controller consumes the request once
and echoes it on `status.backfill.backfilledAt`. It reports the walk (`listed`, `ingested`, `truncated`, `error`) beside
it.

```yaml
spec:
  backfill:
    by: op@example.com
    at: "2026-08-14T12:00:00Z"
    repositories: ["acme/", "acme-labs/tool"] # optional; empty = full scope
```

How the backfill behaves:

- **Filter entries are prefixes** of `owner/name`. `acme/` covers an owner, `acme/service-` a name prefix, and
  `acme/shop` one repository (and any name that extends it). Matching is case-insensitive. An empty filter means
  everything the credential can see.
- **App credentials** fan out across the App's installations. Organization accounts use the org-wide alert listing. User
  accounts, and any account whose filter entries all name repositories, enumerate the installation's repository
  inventory and walk only the matching repositories.
- **PAT credentials** cannot discover repositories, so every filter entry must be an exact `owner/name`. A bare prefix
  is reported as an error on `status.backfill.error`.
- **The walk is bounded** (about 10K alerts per pass, plus a cap on the repository inventory). A pass that exceeds it
  sets `status.backfill.truncated`. Request again with a narrower prefix to cover the rest. There is no cursor to resume
  from.
- **Only open alerts** are listed. Dismissed and fixed alerts are not backfilled.
- The list API leaves out the rule-help markdown that a webhook-driven ingest fetches for each alert. So backfilled
  findings fall back to the rule description. A backfilled secret alert carries only its first detected location, where
  a webhook-driven ingest fetches them all. Everything else (accumulation, labels, tracking issues) behaves the same.

## GitHub Enterprise Server

Point the resource at your instance. Everything else stays the same. The
[Forge](../forges/github.md#github-enterprise-server) takes the same field:

```yaml
spec:
  github:
    baseURL: https://ghes.example.com/api/v3
```

## Egress proxy

`spec.github.proxy` routes all of this integration's GitHub traffic through an HTTP/HTTPS forward proxy. That covers
issue projection, alert fetches, dismissals and resolutions, the delivery sweep, and the backfill. Use it for GitHub
Enterprise Cloud organizations behind an IP allowlist:

```yaml
spec:
  github:
    proxy:
      url: http://proxy.corp.example:3128
```

Basic-auth credentials go in the credential Secret under the optional keys `proxyUsername` and `proxyPassword`. They
never go in the URL. A `spec.github.proxy` overrides the process's `HTTPS_PROXY`/`HTTP_PROXY`/`NO_PROXY` environment for
this integration's traffic. When it is unset, the environment applies.

!!! warning "Set the proxy on the Forge too"

    The integration-controller authenticates through the **Integration**, with the Integration's own Secret and
    `baseURL` rather than the Forge's. So a proxy on the [Forge](../forges/github.md#egress-proxy) alone leaves this
    resource's calls outside the allowlist (the backfill's `list installations` call, for example). If you use an IP
    allowlist, set the proxy on **both** resources.
