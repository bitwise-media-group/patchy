# webhook-controller

The single internet-facing component: validates each GitHub delivery against the shared HMAC secret and routes it to the
controllers that consume its event type. It holds no GitHub credential — the GitHub App flags exist (the binaries share
one flag surface) but are never read.

```sh
webhook-controller serve --webhook-secret-file /etc/patchy/webhook/secret \
  --forward-routes "code_scanning_alert=http://patchy-source-controller:8080/webhook,issues=http://patchy-context-controller:8080/webhook,issue_comment=http://patchy-remediation-controller:8080/webhook,pull_request=http://patchy-remediation-controller:8080/webhook,*=http://patchy-source-controller:8080/webhook"
```

## Flags

The [shared flags](index.md#shared-flags-all-three-controllers) (of which only `--listen-addr`, `--webhook-secret-file`,
and `--log-level` matter here), plus:

<div class="nowrap-first" markdown>

| Flag                | Env                      | Default | Purpose                                                      |
| ------------------- | ------------------------ | ------- | ------------------------------------------------------------ |
| `--forward-routes`  | `PATCHY_FORWARD_ROUTES`  | —       | Comma-separated `event=url` routes (see below). **Required** |
| `--forward-timeout` | `PATCHY_FORWARD_TIMEOUT` | `10s`   | Per-target forward timeout                                   |

</div>

## Routing

Each route is `<X-GitHub-Event>=<absolute http(s) URL>`. Repeating an event fans that event out to every listed target,
and the `*` key catches event types no other route claims — point it at the source-controller, which owns the
`pkg/source` plugin seam, so a plugin's event subscription works without a routing change. An event with no route and no
`*` is logged and dropped (not an error: GitHub only delivers subscribed events, so an unrouted type is a config gap,
and the reconcile loops cover it regardless).

The stock table matches what each binary consumes:

| Event                 | Target                   |
| --------------------- | ------------------------ |
| `code_scanning_alert` | `source-controller`      |
| `issues`              | `context-controller`     |
| `issue_comment`       | `remediation-controller` |
| `pull_request`        | `remediation-controller` |
| `*` (everything else) | `source-controller`      |

## Behavior

- **Validate first** — the standard webhook server (HMAC check, dedup, bounded queue) answers GitHub before any
  forwarding happens; a forged signature never leaves the webhook-controller.
- **Route, signature intact** — the forwarded `X-Hub-Signature-256` is recomputed with the same shared secret, so it is
  byte-identical to GitHub's and every controller still authenticates every delivery itself; the webhook-controller is
  not trusted.
- **Best-effort forwarding** — a route's targets are forwarded concurrently; a failed target is logged and skipped,
  because the controllers' reconcile loops are the retry mechanism. `patchy.webhookctrl.forwards` counts per-target
  results (including `unrouted`).
- **Stateless** — no GitHub client, no Kubernetes API access, safe to run replicated (the chart defaults to 2).
