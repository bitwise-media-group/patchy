# Google Cloud Security Command Center

Patchy ingests Security Command Center findings and triages them alongside code-scanning alerts. A cloud finding is
about a **resource**, not a file, so the pipeline treats it differently in two places: how it accumulates (below), and
how it [finds a repository to work in](../enhancers/google-cai.md).

!!! info "One provider, two capabilities"

    The SCC source on this page and the [Cloud Asset Inventory enhancer](../enhancers/google-cai.md) are independent
    halves of the one `google-cloud` Integration — an estate sourcing findings from Wiz alone still configures the
    enhancer half, without enabling the source. The [integrations overview](../index.md) maps every provider's roles.

## How findings arrive

Security Command Center has no webhook. Its only egress is a `NotificationConfig` publishing to a Pub/Sub topic, so a
**push subscription** forwards each notification to patchy's receiver at `/google-cloud/webhooks`. The
[`scc-notifications` Terraform submodule](https://github.com/bitwise-media-group/terraform-google-gke-flux/tree/main/modules/scc-notifications)
provisions that path — topic, notification config, subscription, dead-letter queue and IAM — and outputs the two values
the `Integration` must agree with.

Authentication is the **OIDC token Pub/Sub signs**, not an HMAC. A push subscription cannot compute one: Pub/Sub
composes the message, so the sender never sees the bytes patchy receives. The token is the stronger primitive anyway —
asymmetric, short-lived, and bound to both a service account and an audience. Patchy checks both, because anyone with a
Google Cloud account can mint a Google-signed token; it is the _identity_ and _audience_ that make one meaningful.

```yaml
apiVersion: patchy.bitwisemedia.uk/v1alpha1
kind: Integration
metadata:
  name: google-cloud
  namespace: patchy
spec:
  provider: google-cloud
  googleCloud:
    securityCommandCenter:
      enabled: true
      # Both must match the push subscription exactly; a mismatch fails closed.
      audience: https://patchy.example.co.uk/google-cloud/webhooks
      serviceAccount: patchy-scc-push@x-patchy-app.iam.gserviceaccount.com
      # Numeric organization id, for composing Console links.
      organization: "123456789012"
      # A backstop under the notification config's own filter.
      minSeverity: high
```

A `google-cloud` Integration carries **no `secretRef`**: it holds no credential, and nothing in it calls a Google API.

Deliveries patchy declines are retried by Pub/Sub and then dead-lettered, never silently dropped. Two signals are worth
alerting on: anything in the dead-letter topic, and the subscription's oldest unacknowledged message age — patchy
answers 503 when its delivery queue is full, which Pub/Sub correctly treats as backpressure.

## What patchy keeps

The notification is self-contained, so ingest makes no API call. From it a `Finding` gets:

| Finding field        | From                                                            |
| -------------------- | --------------------------------------------------------------- |
| `spec.cloudResource` | `finding.resourceName` plus the notification's `resource` block |
| `spec.advisories`    | the CVE when there is one, then `category:<CATEGORY>`           |
| `spec.alerts[].id`   | `finding.name`, the full SCC resource name                      |
| `spec.severity`      | `finding.severity`, lowercased                                  |
| `spec.description`   | the whole notification, rendered to markdown                    |

Notifications are skipped when the finding is not `ACTIVE`, is muted, is an `SCC_ERROR` (a detector that could not run —
an operational problem for whoever owns the SCC configuration, not something to triage), or falls below `minSeverity`.

**Accumulation is per resource, not per project.** SCC re-notifies on every update to a finding, and those
re-notifications fold into one `Finding`. Two different buckets with the same misconfiguration stay separate, because
repository resolution is per resource and a family spanning resources could resolve to two different repositories with
no right answer.

## What is not implemented

**Muting on an `ignore` verdict.** Patchy dismisses a GHAS alert when it judges a finding a false positive; the
equivalent for SCC is muting, which needs `securitycenter.findings.setMute` — a Google Cloud _write_ permission
integration-controller deliberately does not hold. The write-back seam (`pkg/source.Resolver`) is in place and the
`googleCloud.securityCommandCenter.mute` block is accepted and validated but not yet honoured, so enabling it later is
one implementation rather than a refactor.
