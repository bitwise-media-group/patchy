# integration-controller

The single internet-facing entry point, driven by `Integration` custom resources. Inbound: it validates provider
webhooks — `POST /github/webhooks` (per-Integration HMAC secrets), `POST /google-cloud/webhooks` (Pub/Sub-signed OIDC
tokens), `POST /wiz/webhooks` (per-Integration bearer tokens), `POST /generic/<name>/webhooks` (each generic
Integration's own HMAC secret) — and ingests scanner alerts into `Finding` resources — accumulation, duplicate merge.
Outbound: it projects Findings to their tracking issues (body, labels, enrichment and report comments) and applies human
signals (issue close and reopen, `/approve` comments, PR merge/close) back onto Findings. It holds no GitHub credential
itself — the credentials live in the Secrets your Integrations reference, read on demand.

```sh
integration-controller serve --namespace patchy --accumulation-window 1h
```

## Flags

The [shared flags](index.md#shared-flags-all-five-controllers), plus:

| Flag                    | Env                          | Default | Purpose                                                                |
| ----------------------- | ---------------------------- | ------- | ---------------------------------------------------------------------- |
| `--accumulation-window` | `PATCHY_ACCUMULATION_WINDOW` | `1h`    | How long alerts of one finding family accumulate into a single Finding |

## The webhook receiver

One route per provider on `--listen-addr`, each authenticating on the provider's own terms before any handling happens:
`POST /github/webhooks` validates `X-Hub-Signature-256` (constant-time HMAC) against the `webhookSecret` of every
configured github Integration; `POST /google-cloud/webhooks` validates the OIDC token a Pub/Sub push signs against the
SCC Integration's audience and service account; `POST /wiz/webhooks` validates the bearer token against every wiz
Integration's `webhookToken` (also constant-time). The exception to one-route-per-provider is generic: the wildcard
`POST /generic/{name}/webhooks` serves every generic Integration, validating `X-Patchy-Signature-256` against **only**
the named Integration's `webhookSecret` — never the whole candidate set, so one integration's secret cannot admit a
delivery addressed to another. All of them answer the same way:

| Response | Meaning                                                        |
| -------- | -------------------------------------------------------------- |
| `202`    | Accepted and queued (duplicates by delivery ID also get `202`) |
| `204`    | `ping`                                                         |
| `401`    | No Integration's webhook secret matched the signature          |
| `503`    | The delivery queue is full — GitHub redelivers                 |

Bodies are capped at 25 MiB (GitHub's own limit) and the last 1024 delivery IDs are deduplicated. A lost delivery is
never fatal: the reconcile loops are the retry mechanism, and the webhook path only carries ingestion and human signals.

## Behavior

- **Ingestion** — scanner deliveries go through the matching [`pkg/source` handler](../extending.md), which normalizes
  them into findings. A first alert creates a Finding at `Opened`; alerts of the same advisory family against the same
  repository fold into the existing Finding until the accumulation window closes (the `AccumulationComplete` condition —
  accumulation runs concurrently with enhancement, so it is not a phase). Later alerts open a fresh Finding.
- **Projection** — a Finding reconciler renders each Finding to its tracking issue: the templated body, the
  [projected labels](../labels.md#the-projected-labels), enrichments and investigation reports as comments, and
  open/closed state. One-way only.
- **Human signals** — issue close (`→ HandedOff`), issue reopen (`Dismissed → HandedOff`), accepted `/approve` comments
  (recorded on `spec.approval`), and `pull_request` webhooks on `patchy/<finding>` branches (`InReview → Remediated` on
  merge, `→ Failed` on unmerged close).
- **Credential revalidation** — an Integration reconciler validates each Integration's referenced Secret on its
  `spec.interval` and maintains its `Ready` condition.
