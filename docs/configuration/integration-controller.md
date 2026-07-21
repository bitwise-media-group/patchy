# integration-controller

The single internet-facing entry point, driven by `Integration` custom resources. Inbound: it validates provider
webhooks (`POST /github/webhooks`, per-Integration HMAC secrets) and ingests scanner alerts into `Finding` resources —
accumulation, duplicate merge. Outbound: it projects Findings to their tracking issues (body, labels, enrichment and
report comments) and applies human signals (issue close and reopen, `/approve` comments, PR merge/close) back onto
Findings. It holds no GitHub credential itself — the credentials live in the Secrets your Integrations reference, read
on demand.

```sh
integration-controller serve --namespace patchy --accumulation-window 1h
```

## Flags

The [shared flags](index.md#shared-flags-all-five-controllers), plus:

<div class="nowrap-first" markdown>

| Flag                    | Env                          | Default | Purpose                                                                |
| ----------------------- | ---------------------------- | ------- | ---------------------------------------------------------------------- |
| `--accumulation-window` | `PATCHY_ACCUMULATION_WINDOW` | `1h`    | How long alerts of one finding family accumulate into a single Finding |

</div>

## The webhook receiver

`POST /github/webhooks` on `--listen-addr` is the one URL the GitHub App delivers to. Each delivery's
`X-Hub-Signature-256` is HMAC-validated (constant-time) against the `webhookSecret` of every configured Integration —
GitHub gets its answer before any handling happens:

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
