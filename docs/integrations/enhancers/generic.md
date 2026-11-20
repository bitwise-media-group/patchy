# Generic (HTTP)

The `enhance` capability of a `provider: generic` Integration turns **any external HTTP process** into a
[context enhancer](index.md). Patchy POSTs each opened finding's issue view to your endpoint, and the response body is
the enrichment. It contributes the same thing an in-tree
[`pkg/enhance`](../../extending.md#context-enhancers-pkgenhance) plugin would. This is how you connect a real CMDB
without rebuilding patchy. It runs for findings from **every** source, including sources other than your own.

!!! info "One provider, three capabilities"

    The enhancer is one capability of the same `provider: generic` Integration as the
    [inbound source and verdict resolver](../sources/generic.md). That page covers configuration, identity rules and the
    HMAC authentication scheme. The [integrations overview](../index.md) maps every provider's roles.

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

An Integration that only enhances is valid. Leave `source` off entirely. Any number of generic Integrations can enable
`enhance`. The context-controller calls each one for every finding, in name order, after the cloud lookups. Each request
is signed with that Integration's own secret, and each enrichment is attributed to that Integration's name.

Every call is signed with
[the same HMAC scheme as every other generic exchange](../sources/generic.md#authentication-hmac-both-directions):

--8<-- ".snippets/generic-hmac.md"

## The enhancer call

For each newly opened finding, patchy POSTs to `spec.generic.enhance.url`, limited by `timeout` (default 60s):

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
`204` (or `200` with an empty body) when you have nothing to contribute. Otherwise respond `200` with:

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
- `attributes` project as `security-context: k=v` tracking labels. When two enhancers set the same key, the first
  enhancer in the chain wins. Generic integrations run in name order after the cloud enhancers.
- `repository` [resolves a cloud finding that arrived repo-less](index.md). It is honoured only when the finding has no
  repository yet, only once, and only for `provider: "github"`.

Build your endpoint around these rules:

- The call is synchronous and happens once per finding.
- An error or timeout is logged and **skipped**. The finding moves on without your enrichment.
- The exception is a cloud finding with no repository. It [holds and retries](index.md#when-no-repository-resolves) for
  as long as its accumulation window allows. Patchy must not confuse "could not find out" with "no repository exists".
- The finding stores at most 8 enrichments. Any extra ones are dropped, in chain order.
- Each comment is truncated at 16384 characters.

## Testing

`patchy dev enhance` sends a single signed enhancer exchange to your endpoint from a findings payload file. You need no
cluster and no server to host. The [local-testing walkthrough](../sources/generic.md#testing-your-integration-locally)
covers it alongside the full `patchy dev generic` harness.

## Network posture

The context-controller connects to your enhancer endpoint from inside the cluster. The default NetworkPolicy allows
outbound traffic on TCP 443 only. An endpoint on another port needs the `contextController.networkPolicy.extraEgress`
chart value. As with [every generic endpoint](../sources/generic.md#network-posture), treat it as part of your security
boundary and keep it off the public internet where you can.
