# integration-controller

The single internet-facing entry point, driven by `Integration` custom resources. It does two kinds of work.

Inbound, it validates provider webhooks and ingests scanner alerts into `Finding` resources, including accumulation and
duplicate merge. The webhook routes are:

- `POST /github/webhooks` (per-Integration HMAC secrets)
- `POST /google-cloud/webhooks` (Pub/Sub-signed OIDC tokens)
- `POST /wiz/webhooks` (per-Integration bearer tokens)
- `POST /generic/<name>/webhooks` (each generic Integration's own HMAC secret)

Outbound, it projects Findings to their tracking issues (body, labels, enrichment and report comments). It also applies
human signals back onto Findings: issue close and reopen, `/approve` comments, and PR merge or close. It holds no GitHub
credential itself. The credentials live in the Secrets your Integrations reference, and it reads them when it needs
them.

```sh
integration-controller serve --namespace patchy --accumulation-window 1h
```

## Flags

The [shared flags](index.md#shared-flags), plus:

| Flag                              | Env                                    | Default                | Purpose                                                                                                                     |
| --------------------------------- | -------------------------------------- | ---------------------- | --------------------------------------------------------------------------------------------------------------------------- |
| `--accumulation-window`           | `PATCHY_ACCUMULATION_WINDOW`           | `1h`                   | How long alerts of one finding family accumulate into a single Finding                                                      |
| `--projection-concurrency`        | `PATCHY_PROJECTION_CONCURRENCY`        | `2`                    | Findings projected to tracking issues in parallel (each projection may spend GitHub requests)                               |
| `--internal-addr`                 | `PATCHY_INTERNAL_ADDR`                 | none (off)             | Listen address of the in-cluster [repository-access endpoint](#the-repository-access-endpoint); the deployments use `:9792` |
| `--status-server-service-account` | `PATCHY_STATUS_SERVER_SERVICE_ACCOUNT` | `patchy-status-server` | The one ServiceAccount, in `--namespace`, whose token the repository-access endpoint accepts                                |

## The webhook receiver

There is one route per provider on `--listen-addr`. Each route authenticates the request on the provider's own terms
before it does anything else:

- `POST /github/webhooks` validates `X-Hub-Signature-256` (constant-time HMAC) against the `webhookSecret` of every
  configured github Integration.
- `POST /google-cloud/webhooks` validates the OIDC token that a Pub/Sub push signs, against the SCC Integration's
  audience and service account.
- `POST /wiz/webhooks` validates the bearer token against every wiz Integration's `webhookToken` (also constant-time).
- `POST /generic/{name}/webhooks` is the exception to one route per provider. This wildcard route serves every generic
  Integration. It validates `X-Patchy-Signature-256` against **only** the named Integration's `webhookSecret`, never
  against every candidate. So one integration's secret cannot admit a delivery addressed to another.

All of them answer the same way:

| Response | Meaning                                                        |
| -------- | -------------------------------------------------------------- |
| `202`    | Accepted and queued (duplicates by delivery ID also get `202`) |
| `204`    | `ping`                                                         |
| `401`    | No Integration's webhook secret matched the signature          |
| `503`    | The internal delivery queue is full; the delivery is dropped   |

GitHub does not retry a `503` (or any failed delivery) on its own. Enable
[`spec.github.redelivery`](../integrations/sources/github.md#the-failed-delivery-sweep) on the Integration to sweep the
delivery log for anything that never got a `2xx`. Other senders redeliver on their own terms.

Bodies are capped at 25 MiB (GitHub's own limit). Delivery IDs are deduplicated over a window of the last 1024 IDs, and
each is remembered for at most 5 minutes. A lost delivery never loses pipeline progress. The reconcile loops do the
retrying, and the webhook path only carries ingestion and human signals.

## The repository-access endpoint

The status page's [repositories view](../operate/status-page.md#my-repositories) needs to know whether patchy can read a
repository it has never seen. Only this controller holds the Integrations' GitHub credentials, so it answers the
question. `POST /internal/v1/repository-access` runs on its own listener, `--internal-addr`, and never on the
internet-facing webhook port. With `--internal-addr` empty the listener does not start.

Only the status server may call it:

- **The caller's token is checked.** The status server sends a projected ServiceAccount token in
  `X-Patchy-Caller-Token`. The controller checks it with a TokenReview: the audience must be
  `patchy-integration-controller`, and the identity must be `--status-server-service-account` in the controller's own
  namespace. Results are cached for about a minute, denials included. So the controller needs `create` on `tokenreviews`
  (a ClusterRole, because TokenReview is cluster-scoped).
- **A NetworkPolicy limits the sender.** Port 9792 accepts connections from the status server's pods only. See
  [Deploying](https://github.com/bitwise-media-group/patchy/blob/main/deploy/README.md).

For each repository it tries every github Integration that is not suspended and has code or secret scanning alerts
turned on. With an App credential it first looks up the App's installation on the repository; a `404` means the App is
not installed. It then lists one open code scanning alert
(`GET /repos/{owner}/{repo}/code-scanning/alerts?state=open&per_page=1`), and the secret scanning equivalent when that
capability is on. GitHub's answer tells it whether the permission is missing or Advanced Security is off. Each answer is
cached for five minutes per Integration and repository, whether it was good or bad, and for 30 seconds after a temporary
error. So a busy page costs GitHub at most one or two requests per repository every five minutes.

It also finds the `Forge` that covers the repository. For an App credential it reads the installation's `contents`
permission, so the page can warn when patchy could not push a remediation branch. A PAT reports `unknown`. This needs
read access to `forges`.

## Behavior

- **Ingestion**: scanner deliveries go through the matching [`pkg/source` handler](../extending.md), which normalizes
  them into findings. A first alert creates a Finding at `Opened`. Later alerts of the same advisory family against the
  same repository fold into that Finding until the accumulation window closes. The `AccumulationComplete` condition
  tracks this. Accumulation runs at the same time as enhancement, so it is a condition and not a phase. Alerts after the
  window closes open a new Finding.
- **Projection**: a Finding reconciler renders each Finding to its tracking issue. That covers the templated body, the
  [projected labels](../concepts/issue-labels.md), enrichments and investigation reports as comments, and the issue's
  open or closed state. Projection is one-way only.
- **Human signals**: these are issue close (`→ HandedOff`), issue reopen (`Dismissed → HandedOff`), accepted `/approve`
  comments (recorded on `spec.approval`), and `pull_request` webhooks on `patchy/<finding>` branches. A merged pull
  request moves the finding `InReview → Remediated`. A pull request closed without merging moves it `→ Failed`.
- **Credential revalidation**: an Integration reconciler validates each Integration's referenced Secret on its
  `spec.interval` and maintains its `Ready` condition.
