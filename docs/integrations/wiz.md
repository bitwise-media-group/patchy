# Wiz

Patchy ingests two Wiz feeds and triages them alongside code-scanning alerts and SCC findings: **Wiz Issues** (cloud
misconfigurations and toxic combinations, source `wiz-issues`) and **Wiz Defend** (runtime threat detections, source
`wiz-defend`). Both are cloud findings — about a **resource**, not a file — so they arrive repo-less and lean on the
cloud enhancers — [Cloud Asset Inventory](google-cloud-scc.md#finding-a-repository) for Google Cloud resources,
[AWS resource tags](aws.md) for AWS ones, [Azure resource tags](azure.md) for Azure ones — to resolve a repository from
the resource's ownership labels or tags. A Wiz estate typically runs several Integrations side by side: `wiz` to source
findings, `google-cloud` with `cloudAssetInventory` and/or `aws`/`azure` with `resourceTags` to enhance the cloud
findings, and `github` to carry the tracking issues.

## How findings arrive

Wiz delivers through **automation rules**: a rule matches issues or detections and its webhook action POSTs a rendered
body template to patchy's receiver at `/wiz/webhooks`. Both feeds share the one path — an automation action carries no
event-type header, so the receiver discriminates by the body's shape (a top-level `issue` object vs a top-level `threat`
object). Anything that is neither — including the test delivery Wiz sends when the action is saved — is answered `204`
and dropped, so the connectivity check passes without ingesting anything.

Authentication is a **shared bearer token**, not an HMAC: a Wiz automation action sends static headers and cannot sign
the body. Configure the action's authentication as a token (`Authorization: Bearer <token>`), and store the same value
under the `webhookToken` key of the Integration's credential Secret:

```sh
kubectl -n patchy create secret generic patchy-wiz \
  --from-literal=webhookToken="$(openssl rand -hex 32)"
```

```yaml
apiVersion: patchy.bitwisemedia.uk/v1alpha1
kind: Integration
metadata:
  name: wiz
  namespace: patchy
spec:
  provider: wiz
  secretRef:
    name: patchy-wiz
  wiz:
    issues:
      enabled: true
      # A backstop under the automation rule's own severity filter
      # (low|medium|high|critical, default low). Wiz INFORMATIONAL ranks
      # below low, so the default floor drops it.
      # minSeverity: high
    defend:
      enabled: true
      # minSeverity: high
```

Unlike Pub/Sub, Wiz does not dead-letter failed deliveries; it retries briefly and gives up. The `Integration`'s
`spec.replay` has nothing to sweep for Wiz either (there is no delivery log to walk), so a prolonged receiver outage
means re-triggering the automation rule from the Wiz console.

## The body templates (the payload contract)

The webhook body is whatever the automation action's template renders, so **these templates are the contract**: patchy
decodes exactly this shape, and a rule configured differently will be answered `204` (unrecognized shape) or ingest
incompletely. Configure the Issues rule's action body as:

```json
{
  "trigger": {
    "source": "{{triggerSource}}",
    "type": "{{triggerType}}",
    "ruleId": "{{ruleId}}",
    "ruleName": "{{ruleName}}"
  },
  "issue": {
    "id": "{{issue.id}}",
    "status": "{{issue.status}}",
    "severity": "{{issue.severity}}",
    "created": "{{issue.createdAt}}",
    "projects": "{{#issue.projects}}{{name}} {{/issue.projects}}",
    "description": "{{issue.description}}",
    "resolutionRecommendation": "{{issue.resolutionRecommendation}}",
    "url": "{{issue.url}}",
    "control": {
      "id": "{{issue.sourceRule.id}}",
      "name": "{{issue.sourceRule.name}}",
      "description": "{{issue.sourceRule.cloudConfigurationRuleDescription}}",
      "severity": "{{issue.severity}}"
    }
  },
  "entitySnapshot": {
    "id": "{{issue.entitySnapshot.id}}",
    "type": "{{issue.entitySnapshot.type}}",
    "nativeType": "{{issue.entitySnapshot.nativeType}}",
    "name": "{{issue.entitySnapshot.name}}",
    "cloudPlatform": "{{issue.entitySnapshot.cloudPlatform}}",
    "cloudProviderURL": "{{issue.entitySnapshot.cloudProviderURL}}",
    "providerId": "{{issue.entitySnapshot.providerId}}",
    "region": "{{issue.entitySnapshot.region}}",
    "resourceGroupExternalId": "{{issue.entitySnapshot.resourceGroupExternalId}}",
    "subscriptionExternalId": "{{issue.entitySnapshot.subscriptionExternalId}}",
    "subscriptionName": "{{issue.entitySnapshot.subscriptionName}}"
  }
}
```

and the Defend rule's as:

```json
{
  "trigger": {
    "source": "{{triggerSource}}",
    "type": "{{triggerType}}",
    "ruleId": "{{ruleId}}",
    "ruleName": "{{ruleName}}"
  },
  "threat": {
    "id": "{{threat.id}}",
    "name": "{{threat.name}}",
    "description": "{{threat.description}}",
    "severity": "{{threat.severity}}",
    "status": "{{threat.status}}",
    "createdAt": "{{threat.createdAt}}",
    "ruleId": "{{threat.ruleMatch.rule.id}}",
    "ruleName": "{{threat.ruleMatch.rule.name}}",
    "cloudPlatform": "{{threat.cloudPlatform}}",
    "cloudAccounts": ["{{#threat.cloudAccounts}}{{externalId}}{{/threat.cloudAccounts}}"],
    "mitreTactics": ["{{#threat.mitreTactics}}{{id}}{{/threat.mitreTactics}}"],
    "mitreTechniques": ["{{#threat.mitreTechniques}}{{id}}{{/threat.mitreTechniques}}"],
    "detectionIds": ["{{#threat.detections}}{{id}}{{/threat.detections}}"],
    "url": "{{threat.url}}",
    "actors": [{ "id": "{{actor.id}}", "name": "{{actor.name}}", "type": "{{actor.type}}" }],
    "resources": [
      {
        "id": "{{resource.id}}",
        "name": "{{resource.name}}",
        "type": "{{resource.type}}",
        "nativeType": "{{resource.nativeType}}",
        "providerId": "{{resource.providerId}}",
        "region": "{{resource.region}}",
        "cloudPlatform": "{{resource.cloudPlatform}}",
        "subscriptionExternalId": "{{resource.subscriptionExternalId}}"
      }
    ]
  }
}
```

The exact mustache variable names available to an automation rule vary by Wiz release and trigger — treat the right-hand
sides as the intent ("the issue's id", "the entity's providerId") and verify them against your tenant's template editor;
the **left-hand keys and nesting are what patchy parses** and must match verbatim. The recorded fixtures under
`e2e/fixtures/webhooks/wiz.*.json` are hand-built renderings of these templates and double as the decoder's regression
tests.

## What patchy keeps

| Finding field        | Issues                                                   | Defend                                        |
| -------------------- | -------------------------------------------------------- | --------------------------------------------- |
| `spec.cloudResource` | `entitySnapshot` (platform, providerId, region, account) | one per `resources[]` entry                   |
| `spec.advisories`    | `wiz-control:<control.id>`                               | `wiz-rule:<ruleId>`, plus MITRE technique ids |
| `spec.alerts[].id`   | `issue.id`                                               | `threat.id`                                   |
| `spec.severity`      | `issue.severity`, lowercased                             | `threat.severity`, lowercased                 |
| `spec.description`   | the delivery, rendered to markdown                       | the delivery, rendered to markdown            |

- **Platform mapping.** `cloudPlatform` GCP/AWS/Azure become cloud providers `google`/`aws`/`azure`. Findings on any
  other platform (Kubernetes, OCI, ...) are skipped in v1 — there is no provider value the rest of the pipeline could
  act on.
- **GCP identifiers are normalized at ingest.** Wiz reports Google Cloud resources by API self-link
  (`https://www.googleapis.com/compute/v1/projects/...`); patchy rewrites that to the Cloud Asset Inventory name form
  (`//compute.googleapis.com/projects/...`) so the asset-inventory enhancer can resolve ownership labels. An identifier
  that cannot be rewritten still ingests — the enhancer falls back to a display-name lookup, accepting only an
  unambiguous single hit. AWS and Azure identifiers need no such rewriting: `providerId` is the ARN or the ARM resource
  ID, kept verbatim, and it is exactly what the [AWS](aws.md) and [Azure](azure.md) resource-tags enhancers look up.
- **Accumulation is per resource and per control/rule.** Re-notifications of the same control on the same resource fold
  into one `Finding`; a Defend threat naming several resources becomes one Finding per resource, each accumulating where
  it belongs. A threat naming no resources falls back to one Finding per cloud account.
- **Skipped deliveries.** Trigger types other than Created/Updated/Reopened (notably `Resolved`), statuses other than
  OPEN/IN_PROGRESS, and severities below `minSeverity`. An inbound `Resolved` does **not** yet resolve the patchy
  Finding — future work, alongside SCC's mute.

Defend findings track like any other, but a runtime detection is rarely something a coding agent can remediate with a
pull request: expect them to route to humans (the investigation gate hands off findings with no repository, and a
threat's "fix" is usually response, not code).

## Write-back (optional)

With the `api` block configured, a finding the pipeline **dismisses** (an `ignore` verdict) rejects the originating Wiz
issue — status `REJECTED`, resolution reason `FALSE_POSITIVE` or `WONT_FIX`, and the verdict's explanation as the note.
Without the block, ingestion is one-way, exactly like SCC. Defend threats are never written back.

```yaml
spec:
  wiz:
    api:
      # Your tenant's GraphQL endpoint (Settings → Tenant Info).
      endpoint: https://api.eu1.app.wiz.io/graphql
      # tokenURL: https://auth.app.wiz.io/oauth/token   # non-commercial partitions differ
```

The credential is a Wiz **service account** (type "Custom Integration") with `update:issues` scope; add its keys to the
same Secret:

```sh
kubectl -n patchy patch secret patchy-wiz -p '{"stringData": {
  "clientId": "<service account client id>",
  "clientSecret": "<service account client secret>"
}}'
```

## Replaying fixtures

The [replay tool](../configuration/integration-controller.md) infers the route from the fixture name and sends the token
as the bearer credential:

```sh
mise run replay -- -bearer "$WIZ_WEBHOOK_TOKEN" fixtures/webhooks/wiz.issue.created.json
```
