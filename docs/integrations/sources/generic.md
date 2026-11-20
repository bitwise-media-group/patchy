# Generic (HTTP)

The `generic` provider registers **any external HTTP process** as a patchy integration. Examples include a scheduled job
that queries a warehouse store and pushes what it found, an internal CMDB, or a bridge in front of a vendor that has no
supported provider. It exists for sources that are **not event-driven**. Nothing has to deliver webhooks on the source
system's schedule; the process POSTs findings when it chooses.

A generic integration has three capabilities, and you enable each one separately:

- **Source**: an inbound findings webhook at `/generic/<integration-name>/webhooks` (this page).
- **Resolver** (a sub-block of source): when a finding is dismissed, patchy POSTs the triage verdict back so the source
  system can close its own record (this page).
- **Enhancer**: patchy POSTs each opened finding's issue view to the process, and the response body becomes the
  enrichment ([its own page](../enhancers/generic.md)).

!!! info "One provider, three capabilities"

    All three are capabilities of the same `provider: generic` Integration. Configuration, identity and HMAC
    authentication are described on this page. The [enhancer call](../enhancers/generic.md) has its own page. The
    [integrations overview](../index.md) maps every provider's roles.

Generic differs from every other provider because it is **not a namespace singleton**. You can define one generic
Integration for each process you have. Each one is its own identity. Its **name** is used as:

- the finding source id (the `security-source` label)
- the enrichment attribution on the tracking issue
- the resolver dispatch key
- the webhook path segment

For the same reason, a generic Integration cannot use the name of a built-in source id (`ghas`, `gcp-scc`, `wiz-issues`,
`wiz-defend`), and its name must fit in a label value (63 characters).

This page describes the payload contract. The Go types live in
[`pkg/generic`](https://github.com/bitwise-media-group/patchy/tree/main/pkg/generic), a public package that an external
process written in Go can import directly. Every shape below round-trips through it. All bodies are JSON, and the
contract version is `v1`.

## Configuration

```yaml
apiVersion: patchy.bitwisemedia.uk/v1alpha1
kind: Integration
metadata:
  name: warehouse # the identity: source id, label value, path segment
  namespace: patchy
spec:
  provider: generic
  secretRef:
    name: patchy-warehouse # key webhookSecret: the shared HMAC secret
  generic:
    source:
      enabled: true # inbound findings at /generic/warehouse/webhooks
      # minSeverity: high    # low|medium|high|critical, default low
      resolver:
        enabled: true # verdict write-back on dismissal
        url: https://warehouse.internal/patchy/resolve
        timeout: 60s
    enhance:
      enabled: true # synchronous enrichment call per opened finding
      url: https://warehouse.internal/patchy/enhance
      timeout: 60s
```

```sh
kubectl -n patchy create secret generic patchy-warehouse \
  --from-literal=webhookSecret="$(openssl rand -hex 32)"
```

The Integration's `status.webhookPath` shows the concrete inbound path. Pair a source with a
[`github` Integration](github.md) that has issues enabled, because findings from a generic source get their tracking
issues through it. An integration can be a source only, an enhancer only, or both. A `generic` block that enables
neither fails validation.

## Authentication: HMAC both directions

Every exchange carries the same signature. This covers inbound deliveries and patchy's outbound resolver and enhancer
calls:

--8<-- ".snippets/generic-hmac.md"

For inbound deliveries, patchy checks the signature against **only** the Integration named in the path. Integration A's
secret never validates a delivery addressed to B. An unknown name, a suspended integration, and a source-disabled one
all answer `401`, and you cannot tell them apart. For outbound calls, patchy signs what it sends, and your process
should verify the signature the same way.

An inbound delivery can also carry `X-Patchy-Delivery: <unique id>` for deduplication. The window is 5 minutes, scoped
to the integration's own path. Without this header, patchy derives the id from the body hash. Byte-identical
redeliveries are still deduplicated, and distinct payloads never are.

## Inbound: the findings payload

`POST /generic/<name>/webhooks` with a versioned envelope:

```json
{
  "version": "v1",
  "event": "findings",
  "findings": [
    {
      "repo": { "owner": "acme", "name": "orders" },
      "alertId": "wh-1001",
      "advisories": ["CVE-2026-4242", "CWE-89"],
      "ruleId": "warehouse/sql-injection",
      "title": "SQL injection in the nightly export",
      "description": "The export job interpolates user-controlled column names…",
      "severity": "high",
      "htmlUrl": "https://warehouse.internal/findings/1001",
      "locations": [{ "path": "jobs/export.go", "start_line": 88, "end_line": 92 }]
    }
  ]
}
```

`event: "ping"` is the connectivity test. It returns `204` and ingests nothing. A findings delivery returns `202`
(queued). Ingestion is asynchronous, and the reconcile loop handles retries.

Per finding:

| Field           | Required | Meaning                                                                                                                                                  |
| --------------- | -------- | -------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `repo`          | see note | `{owner, name}`: the repository the finding is about. Omit for a cloud finding.                                                                          |
| `cloudResource` | see note | `{provider, name, type?, project?, location?, display_name?}`: the cloud resource it is about.                                                           |
| `alertId`       | see note | The finding's identifier in your system. Takes precedence over `alertNumber`. Echoed back on write-back.                                                 |
| `alertNumber`   | see note | A positive source-native number, for tools that number findings instead of naming them.                                                                  |
| `advisories`    | no       | Categorization identifiers, **most authoritative first**. `advisories[0]` is the accumulation key (you choose the order, e.g. GHSA, then CVE, then CWE). |
| `ruleId`        | no       | Your tool's rule identifier.                                                                                                                             |
| `title`         | **yes**  | One-line human summary.                                                                                                                                  |
| `description`   | no       | Full markdown. It becomes the tracking issue's finding section word for word. You write it, because patchy has no template for your tool.                |
| `severity`      | **yes**  | `low`, `medium`, `high`, or `critical`. Normalize before delivering.                                                                                     |
| `htmlUrl`       | no       | Link back to the finding in your system.                                                                                                                 |
| `locations`     | no       | `{path, start_line?, end_line?, snippet?}`, repository-relative.                                                                                         |

Patchy applies these validation rules to every finding in the batch. One invalid finding rejects the delivery, with all
the errors joined. Fix the problem and re-POST; patchy never accepts half a batch silently.

- A finding names a `repo`, a `cloudResource`, or both. A finding with neither has nothing to accumulate against.
- `alertId` or a positive `alertNumber` is required. Write-back and duplicate merging both key on this identity.
- If `advisories` is empty, patchy falls back to `generic-rule:<ruleId>`, or else `generic-alert:<id>`. This way
  findings of the same rule still fold together during [accumulation](../../concepts/how-it-works.md).
- Patchy silently drops findings below the Integration's `minSeverity`. It does not reject them.

Cloud findings arrive without a repository. They rely on the [context enhancers](../enhancers/index.md) (or your own
[generic enhancer](../enhancers/generic.md)) to resolve one, the same way [Wiz](wiz.md) and [SCC](google-scc.md)
findings do.

## Outbound: the resolver call

When a finding built from alerts you delivered is **dismissed** (the investigation judged it a false positive or not
exploitable), patchy POSTs to `spec.generic.source.resolver.url`:

```json
{
  "version": "v1",
  "integration": "warehouse",
  "alerts": [{ "id": "wh-1001", "url": "https://warehouse.internal/findings/1001" }],
  "verdict": { "kind": "ignore", "reason": "false positive", "comment": "Dismissed by patchy: …" }
}
```

Any `2xx` answer counts as success. **Your endpoint must be idempotent.** Patchy sends once per dismissal but retries on
failure, so the same alerts may be resolved more than once. Treat "already closed" as success. `ignore` is the only
verdict kind today. A source with the resolver off is still a complete source; dismissal skips the write-back.

## Testing your integration locally

The `patchy` CLI ships a harness that tests all three exchanges against your process without a cluster.
`patchy dev generic` hosts the real receiver: the same server, HMAC authentication, deduplication, and validation that
the integration-controller runs. It keeps ingested findings in memory. Straight after ingestion, it makes the enhancer
call and the resolver write-back to the endpoints you configure:

```sh
# Terminal A: the harness (omit the secret to have one generated and printed):
patchy dev generic --secret dev-secret \
  --enhance-url http://127.0.0.1:9000/enhance \
  --resolve-url http://127.0.0.1:9000/resolve

# Terminal B: deliver a signed payload:
sig="sha256=$(openssl dgst -sha256 -hmac dev-secret -r < findings.json | cut -d' ' -f1)"
curl -i -X POST http://127.0.0.1:8100/generic/dev/webhooks \
  -H 'Content-Type: application/json' \
  -H "X-Patchy-Signature-256: $sig" \
  --data-binary @findings.json
```

A process that is **only an enhancer or resolver** never delivers findings, so there is nothing to host. Instead, the
one-shot commands send a single signed exchange built from a findings payload. The payload uses the same envelope the
webhook accepts and is validated by the same code:

```sh
patchy dev enhance --url http://127.0.0.1:9000/enhance --secret dev-secret findings.json
patchy dev resolve --url http://127.0.0.1:9000/resolve --secret dev-secret findings.json
```

You can set every `dev` flag in three places:

- on the command line
- through a `PATCHY_DEV_*` environment variable (`PATCHY_DEV_ENHANCE_URL`, …)
- in the `dev:` block of the [patchy configuration](../../supply-chain/configuration.md): `patchy.yaml` in the working
  directory, or a layer in `~/.config/patchy/`

Explicit flags win over the environment, and the environment wins over the file:

```yaml
dev:
  name: warehouse
  secret-file: ./webhook.secret
  enhance-url: http://127.0.0.1:9000/enhance
  resolve-url: http://127.0.0.1:9000/resolve
```

The harness does **not** emulate these parts of the pipeline:

- accumulation and duplicate-merge into Finding resources
- tracking issues
- the investigation between enhancement and dismissal

The resolve call fires straight after enhancement. In production it is sent only at dismissal, usually much later. It
also carries one alert per call, where production sends a finding's accumulated set. Rejections answer `401` with no
reason on the wire, the same as in production. The harness writes the reason to its stderr log instead. `-o json` writes
one JSON event per line on stdout for piping into `jq`.

## Network posture

Patchy's integration-controller calls your resolver endpoint from inside the cluster. (The
[enhancer call](../enhancers/generic.md#network-posture) comes from the context-controller.) The default NetworkPolicies
allow egress on TCP 443 only. An endpoint on another port needs the matching chart value
(`integrationController.networkPolicy.extraEgress`). The URLs come from a CR that only operators can write, and patchy
never follows redirects into the cluster on your behalf. Still, treat the endpoints as part of your security boundary,
and keep them off the public internet where you can.

## Limitations

- The delivery dedup window is 5 minutes. A redelivery after that is ingested again. (Ingestion itself is idempotent:
  the redelivery folds into the existing finding.)
- Patchy writes back only the `ignore` verdict. It does not report a remediated finding to the source system today.
