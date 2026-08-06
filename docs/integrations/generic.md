# Generic (HTTP)

The `generic` provider registers **any external HTTP process** as a patchy integration — a scheduled job that queries a
warehouse store and pushes whatever it found, an internal CMDB, a bridge in front of a vendor without a supported
provider. It exists for sources that are **not event-driven**: nothing has to deliver webhooks on the source system's
schedule, the process POSTs findings on its own.

A generic integration has three capabilities, each independently enabled:

- **Source** — an inbound findings webhook at `/generic/<integration-name>/webhooks`.
- **Resolver** (a sub-block of source) — patchy POSTs the triage verdict back when a finding is dismissed, so the source
  system can close its own record.
- **Enhancer** — patchy POSTs each opened finding's issue view to the process and the response body is the enrichment,
  exactly what an in-tree [context enhancer](../extending.md) would contribute.

Unlike every other provider, generic is **not a namespace singleton**: define as many generic Integrations as you have
processes. Each one is its own identity — its **name** is the finding source id (the `security-source` label), the
enrichment attribution on the tracking issue, the resolver dispatch key, and the webhook path segment. That is also why
a generic Integration may not be named after a built-in source id (`ghas`, `gcp-scc`, `wiz-issues`, `wiz-defend`) and
must fit in a label value (63 characters).

This page is the payload contract. The Go types live in
[`pkg/generic`](https://github.com/bitwise-media-group/patchy/tree/main/pkg/generic) — a public package an external
process written in Go can import directly; every shape below round-trips through it. All bodies are JSON; the contract
version is `v1`.

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

The Integration's `status.webhookPath` advertises the concrete inbound path. Pair a source with a `github` Integration
(issues enabled): findings from a generic source get their tracking issues through it. A source may be a pure source, a
pure enhancer, or both; a `generic` block enabling neither fails validation.

## Authentication: HMAC both directions

Every exchange — inbound deliveries and patchy's outbound resolver/enhancer calls — carries an HMAC-SHA256 signature
over the **raw request body**, keyed with the shared secret under the credential Secret's `webhookSecret` key:

```text
X-Patchy-Signature-256: sha256=<hex of HMAC-SHA256(secret, body)>
```

Inbound, patchy verifies the signature against **only** the Integration the path names — integration A's secret never
validates a delivery addressed to B, and an unknown name, a suspended integration, or a source-disabled one all answer
`401` indistinguishably. Outbound, patchy signs what it sends and your process should verify the same way (constant-time
comparison, computed over the exact bytes received). Signing in Go:

```go
mac := hmac.New(sha256.New, secret)
mac.Write(body)
sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
```

An inbound delivery may also carry `X-Patchy-Delivery: <unique id>` for deduplication (a 5-minute window, scoped to the
integration's own path). Without it, patchy derives the id from the body hash, so byte-identical redeliveries still
dedup and distinct payloads never do.

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

`event: "ping"` is the connectivity test: answered `204`, nothing ingested. A findings delivery answers `202` (queued) —
ingestion is asynchronous, and the reconcile loop is the retry mechanism.

Per finding:

| Field           | Required | Meaning                                                                                                                                         |
| --------------- | -------- | ----------------------------------------------------------------------------------------------------------------------------------------------- |
| `repo`          | see note | `{owner, name}` — the repository the finding is about. Omit for a cloud finding.                                                                |
| `cloudResource` | see note | `{provider, name, type?, project?, location?, display_name?}` — the cloud resource it is about.                                                 |
| `alertId`       | see note | The finding's identifier in your system; supersedes `alertNumber`. Echoed back on write-back.                                                   |
| `alertNumber`   | see note | A positive source-native number, for tools that number rather than name findings.                                                               |
| `advisories`    | no       | Categorization identifiers, **most authoritative first** — `advisories[0]` is the accumulation key (GHSA over CVE over CWE, by your judgement). |
| `ruleId`        | no       | Your tool's rule identifier.                                                                                                                    |
| `title`         | **yes**  | One-line human summary.                                                                                                                         |
| `description`   | no       | Full markdown; becomes the tracking issue's finding section verbatim. You author it — patchy has no template for your tool.                     |
| `severity`      | **yes**  | `low`, `medium`, `high`, or `critical` — normalize before delivering.                                                                           |
| `htmlUrl`       | no       | Link back to the finding in your system.                                                                                                        |
| `locations`     | no       | `{path, start_line?, end_line?, snippet?}`, repository-relative.                                                                                |

Validation rules, enforced on the whole batch (one invalid finding rejects the delivery with the errors joined, so fix
and re-POST rather than half a batch silently landing):

- A finding names a `repo`, a `cloudResource`, or both — one with neither has nothing to accumulate against.
- `alertId` or a positive `alertNumber` is required — the identity write-back and duplicate-merge key on.
- Empty `advisories` fall back to `generic-rule:<ruleId>`, else `generic-alert:<id>`, so findings of the same rule still
  fold together during [accumulation](../how-it-works.md).
- Findings below the Integration's `minSeverity` are dropped silently, not rejected.

Cloud findings arrive repo-less and lean on the cloud enhancers (or your own generic enhancer) to resolve a repository,
exactly like [Wiz](wiz.md) and [SCC](google-cloud-scc.md) findings.

## Outbound: the enhancer call

For each freshly opened finding — from **any** source, not just your own — patchy POSTs to `spec.generic.enhance.url`,
signed as above, bounded by `timeout` (default 60s):

```json
{
  "version": "v1",
  "integration": "warehouse",
  "issue": {
    "repo": { "owner": "acme", "name": "orders" },
    "number": 17,
    "title": "SQL injection in the nightly export",
    "body": "…finding description markdown…",
    "labels": ["security-finding: opened"],
    "cloudResource": null
  }
}
```

`integration` names the Integration the call is for, so one process can serve several from a single endpoint. Respond
`204` (or `200` with an empty body) when you have nothing to contribute, else `200` with:

```json
{
  "owners": ["alice", "bob"],
  "commentMarkdown": "Owned by team-warehouse; runbook: https://…",
  "attributes": { "system": "warehouse", "tier": "1" },
  "repository": { "provider": "github", "url": "https://github.com/acme/orders" }
}
```

- `owners` drive tracking-issue assignment, in preference order.
- `commentMarkdown` becomes one sticky comment per integration on the tracking issue, edited in place on change.
- `attributes` project as `security-context: k=v` tracking labels; on key collisions the first enhancer in the chain
  wins, and generic integrations run in name order after the cloud enhancers.
- `repository` resolves a cloud finding that arrived repo-less. It is honoured only when the finding has no repository
  yet, only once, and only for `provider: "github"`.

Semantics to build against: the call is synchronous and per-finding; an error or timeout is logged and **skipped** — the
finding advances without your enrichment — except a repo-less cloud finding, which holds and retries for as long as its
accumulation window allows, because "could not find out" must not be confused with "no repository exists". The finding
stores at most 8 enrichments (excess drop in chain order) and truncates each comment at 16384 characters.

## Outbound: the resolver call

When a finding whose alerts you delivered is **dismissed** (the investigation judged it a false positive or not
exploitable), patchy POSTs to `spec.generic.source.resolver.url`:

```json
{
  "version": "v1",
  "integration": "warehouse",
  "alerts": [{ "id": "wh-1001", "url": "https://warehouse.internal/findings/1001" }],
  "verdict": { "kind": "ignore", "reason": "false positive", "comment": "Dismissed by patchy: …" }
}
```

Any `2xx` answer is success. **Idempotency is your obligation**: patchy sends once per dismissal but retries on failure,
so the same alerts may be resolved more than once — treat "already closed" as success. `ignore` is the only verdict kind
today. A source with the resolver off is a complete source; dismissal simply skips the write-back.

## Testing your integration locally

The `patchy` CLI ships a harness that exercises all three exchanges against your process without a cluster.
`patchy dev generic` hosts the real receiver — the same server, HMAC authentication, deduplication, and validation the
integration-controller runs — keeps ingested findings in memory, and immediately drives the enhancer call and the
resolver write-back at the endpoints you configure:

```sh
# Terminal A — the harness (omit the secret to have one generated and printed):
patchy dev generic --secret dev-secret \
  --enhance-url http://127.0.0.1:9000/enhance \
  --resolve-url http://127.0.0.1:9000/resolve

# Terminal B — deliver a signed payload:
sig="sha256=$(openssl dgst -sha256 -hmac dev-secret -r < findings.json | cut -d' ' -f1)"
curl -i -X POST http://127.0.0.1:8100/generic/dev/webhooks \
  -H 'Content-Type: application/json' \
  -H "X-Patchy-Signature-256: $sig" \
  --data-binary @findings.json
```

A process that is **only an enhancer or resolver** never delivers findings, so there is nothing to host: the one-shot
commands fire a single signed exchange from a findings payload instead (the same envelope the webhook accepts, validated
by the same code):

```sh
patchy dev enhance --url http://127.0.0.1:9000/enhance --secret dev-secret findings.json
patchy dev resolve --url http://127.0.0.1:9000/resolve --secret dev-secret findings.json
```

Every `dev` flag also resolves from a `PATCHY_DEV_*` environment variable (`PATCHY_DEV_ENHANCE_URL`, …) and an optional
`.patchy.yaml`/`.yml`/`.json` in the working directory, with explicit flags winning over the environment and the
environment over the file:

```yaml
dev:
  name: warehouse
  secret-file: ./webhook.secret
  enhance-url: http://127.0.0.1:9000/enhance
  resolve-url: http://127.0.0.1:9000/resolve
```

What the harness deliberately does **not** emulate: accumulation and duplicate-merge into Finding resources, tracking
issues, and the investigation between enhancement and dismissal. The resolve call fires immediately after enhancement
(production sends it only at dismissal, typically much later) and carries one alert per call rather than a finding's
accumulated set. Rejections answer `401` with no reason on the wire, exactly as in production — the reason appears in
the harness's stderr log instead. `-o json` emits one JSON event per line on stdout for piping into `jq`.

## Network posture

Patchy's controllers dial your endpoints from inside the cluster: the resolver from the integration-controller, the
enhancer from the context-controller. The default NetworkPolicies allow egress on TCP 443 only — an endpoint on another
port needs the corresponding chart value (`integrationController.networkPolicy.extraEgress` /
`contextController.networkPolicy.extraEgress`). The URLs come from a CR only operators can write, and patchy never
follows redirects into the cluster on your behalf — but treat the endpoints as part of your security boundary and keep
them off the public internet where you can.

## Limitations

- The delivery dedup window is 5 minutes; a redelivery after that ingests again (ingestion itself is idempotent — it
  folds into the existing finding).
- `repository` resolution accepts `provider: "github"` only.
- Only the `ignore` verdict is written back; a remediated finding is not reported to the source system today.
