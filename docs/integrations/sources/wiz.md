# Wiz

Patchy ingests two Wiz feeds and triages them alongside code-scanning alerts and SCC findings:

- **Wiz Issues**: cloud misconfigurations and toxic combinations, source `wiz-issues`
- **Wiz Defend**: runtime threat detections, source `wiz-defend`

Both are cloud findings. They describe a **resource**, not a file, so they arrive with no repository. Patchy relies on
the [context enhancers](../enhancers/index.md) to find a repository from the resource's ownership labels or tags:

- [Google Cloud Asset Inventory](../enhancers/google-cai.md) for Google Cloud resources
- [AWS resource tags](../enhancers/aws.md) for AWS resources
- [Azure resource tags](../enhancers/azure.md) for Azure resources

A Wiz setup usually runs several Integrations side by side:

- `wiz` to source findings
- `google-cloud` with `cloudAssetInventory`, and/or `aws`/`azure` with `resourceTags`, to enhance the cloud findings
- [`github`](github.md) to carry the tracking issues

## How findings arrive

Wiz delivers through **automation rules**. A rule matches issues or detections, and its webhook action POSTs a rendered
body template to patchy's receiver at `/wiz/webhooks`. Both feeds share this one path. An automation action sends no
event-type header, so the receiver tells the feeds apart by the body's shape: a top-level `issue` object or a top-level
`threat` object. The receiver answers anything else with `204` and drops it. That includes the test delivery Wiz sends
when you save the action, so the connectivity check passes without ingesting anything.

Authentication uses a **shared bearer token** instead of an HMAC, because a Wiz automation action sends static headers
and cannot sign the body. Set the action's authentication to a token (`Authorization: Bearer <token>`). Store the same
value under the `webhookToken` key of the Integration's credential Secret:

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

Unlike Pub/Sub, Wiz does not dead-letter failed deliveries. It retries for a short time and then gives up. The
`Integration`'s `spec.replay` has nothing to sweep for Wiz either, because there is no delivery log to walk. After a
long receiver outage, re-trigger the automation rule from the Wiz console.

## The body templates (the payload contract)

The webhook body is whatever the automation action's template renders, so **these templates are the contract**. Patchy
decodes only this shape. A rule configured differently gets a `204` (unrecognized shape) or ingests incomplete data.

??? note "The Issues rule's action body"

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

??? note "The Defend rule's action body"

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

The mustache variable names an automation rule can use vary by Wiz release and trigger. Treat the right-hand sides as
the intent ("the issue's id", "the entity's providerId") and check them in your tenant's template editor. Patchy parses
the **left-hand keys and nesting**, so these must match character for character. The recorded fixtures under
`e2e/fixtures/webhooks/wiz.*.json` are hand-built renderings of these templates. They also serve as the decoder's
regression tests.

## What patchy keeps

| Finding field        | Issues                                                   | Defend                                        |
| -------------------- | -------------------------------------------------------- | --------------------------------------------- |
| `spec.cloudResource` | `entitySnapshot` (platform, providerId, region, account) | one per `resources[]` entry                   |
| `spec.advisories`    | `wiz-control:<control.id>`                               | `wiz-rule:<ruleId>`, plus MITRE technique ids |
| `spec.alerts[].id`   | `issue.id`                                               | `threat.id`                                   |
| `spec.severity`      | `issue.severity`, lowercased                             | `threat.severity`, lowercased                 |
| `spec.description`   | the delivery, rendered to markdown                       | the delivery, rendered to markdown            |

- **Platform mapping.** `cloudPlatform` GCP/AWS/Azure become the cloud providers `google`/`aws`/`azure`. In v1, patchy
  skips findings on any other platform (Kubernetes, OCI, ...). There is no provider value the rest of the pipeline could
  act on.
- **GCP identifiers are normalized at ingest.** Wiz reports Google Cloud resources by API self-link
  (`https://www.googleapis.com/compute/v1/projects/...`). Patchy rewrites that to the Cloud Asset Inventory name form
  (`//compute.googleapis.com/projects/...`) so the [asset-inventory enhancer](../enhancers/google-cai.md) can look up
  ownership labels. An identifier that cannot be rewritten is still ingested. The enhancer then falls back to a lookup
  by display name and accepts only a single, unambiguous match. AWS and Azure identifiers need no rewriting.
  `providerId` is the ARN or the ARM resource ID, kept as it is, and the [AWS](../enhancers/aws.md) and
  [Azure](../enhancers/azure.md) resource-tags enhancers look up that value.
- **Accumulation is per resource and per control/rule.** Repeat notifications of the same control on the same resource
  merge into one `Finding`. A Defend threat that names several resources becomes one Finding per resource, and each
  accumulates separately. A threat that names no resources falls back to one Finding per cloud account.
- **Skipped deliveries.** Patchy skips trigger types other than Created/Updated/Reopened (for example `Resolved`),
  statuses other than OPEN/IN_PROGRESS, and severities below `minSeverity`. An inbound `Resolved` does **not** yet
  resolve the patchy Finding. This is future work, along with [SCC's mute](google-scc.md#what-is-not-implemented).

Defend findings are tracked like any other. But a coding agent can rarely fix a runtime detection with a pull request,
so expect them to go to humans. The investigation gate hands off findings with no repository, and the "fix" for a threat
is usually an incident response rather than a code change.

## Write-back (optional)

If you configure the `api` block, a finding the pipeline **dismisses** (an `ignore` verdict) rejects the original Wiz
issue. Patchy sets status `REJECTED`, resolution reason `FALSE_POSITIVE` or `WONT_FIX`, and the verdict's explanation as
the note. Without the block, ingestion is one-way, the same as SCC. Defend threats are never written back.

```yaml
spec:
  wiz:
    api:
      # Your tenant's GraphQL endpoint (Settings → Tenant Info).
      endpoint: https://api.eu1.app.wiz.io/graphql
      # tokenURL: https://auth.app.wiz.io/oauth/token   # non-commercial partitions differ
```

The credential is a Wiz **service account** (type "Custom Integration") with `update:issues` scope. Add its keys to the
same Secret:

```sh
kubectl -n patchy patch secret patchy-wiz -p '{"stringData": {
  "clientId": "<service account client id>",
  "clientSecret": "<service account client secret>"
}}'
```

## Replaying fixtures

The [replay tool](../../deployment/colima.md#apply-the-dev-overlay) works out the route from the fixture name and sends
the token as the bearer credential:

```sh
mise run replay -- -bearer "$WIZ_WEBHOOK_TOKEN" fixtures/webhooks/wiz.issue.created.json
```
