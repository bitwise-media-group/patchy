# Generic (HTTP)

The `enhance` capability of a `provider: generic` Integration turns **any external HTTP process** into a
[context enhancer](index.md): patchy POSTs each opened finding's issue view to your endpoint, and the response body is
the enrichment — exactly what an in-tree [`pkg/enhance`](../../extending.md#context-enhancers-pkgenhance) plugin would
contribute. It is how a real CMDB integrates without a rebuild, and it runs for findings from **every** source, not just
your own.

!!! info "One provider, three capabilities"

    The enhancer is one capability of the same `provider: generic` Integration as the
    [inbound source and verdict resolver](../sources/generic.md) — configuration, identity rules and the HMAC
    authentication scheme live on that page. The [integrations overview](../index.md) maps every provider's roles.

## Configuration

The `enhance` block on the Integration ([full configuration](../sources/generic.md#configuration)):

```yaml
spec:
  provider: generic
  secretRef:
    name: patchy-warehouse # key webhookSecret: the shared HMAC secret
  generic:
    enhance:
      enabled: true # synchronous enrichment call per opened finding
      url: https://warehouse.internal/patchy/enhance
      timeout: 60s
```

A pure enhancer is a valid Integration — leave `source` off entirely. N generic Integrations may all enable `enhance`:
the context-controller calls each one per finding, in name order, after the cloud lookups, each request signed with that
Integration's own secret and each enrichment attributed to that Integration's name.

Every call is signed with
[the same HMAC scheme as every other generic exchange](../sources/generic.md#authentication-hmac-both-directions):

--8<-- ".snippets/generic-hmac.md"

## The enhancer call

For each freshly opened finding patchy POSTs to `spec.generic.enhance.url`, bounded by `timeout` (default 60s):

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
- `repository` [resolves a cloud finding that arrived repo-less](index.md). It is honoured only when the finding has no
  repository yet, only once, and only for `provider: "github"`.

Semantics to build against: the call is synchronous and per-finding; an error or timeout is logged and **skipped** — the
finding advances without your enrichment — except a repo-less cloud finding, which
[holds and retries](index.md#when-no-repository-resolves) for as long as its accumulation window allows, because "could
not find out" must not be confused with "no repository exists". The finding stores at most 8 enrichments (excess drop in
chain order) and truncates each comment at 16384 characters.

## Testing

`patchy dev enhance` fires a single signed enhancer exchange at your endpoint from a findings payload file — no cluster,
no server to host. The [local-testing walkthrough](../sources/generic.md#testing-your-integration-locally) covers it
alongside the full `patchy dev generic` harness.

## Network posture

The context-controller dials your enhancer endpoint from inside the cluster; the default NetworkPolicy allows egress on
TCP 443 only, and an endpoint on another port needs the `contextController.networkPolicy.extraEgress` chart value. As
with [every generic endpoint](../sources/generic.md#network-posture), treat it as part of your security boundary and
keep it off the public internet where you can.
