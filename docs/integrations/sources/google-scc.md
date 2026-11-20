# Google Cloud Security Command Center

Patchy ingests Security Command Center findings and triages them alongside code-scanning alerts. A cloud finding is
about a **resource** rather than a file. So the pipeline treats it differently in two places: how it accumulates
(below), and how it [finds a repository to work in](../enhancers/google-cai.md).

!!! info "One provider, two capabilities"

    The SCC source on this page and the [Google Cloud Asset Inventory enhancer](../enhancers/google-cai.md) are
    independent halves of the one `google-cloud` Integration. If you get findings only from Wiz, you can still configure
    the enhancer half without enabling the source. The [integrations overview](../index.md) maps every provider's
    roles.

## How findings arrive

Security Command Center has no webhook. The only way it sends findings out is a `NotificationConfig` that publishes to a
Pub/Sub topic. A **push subscription** then forwards each notification to patchy's receiver at `/google-cloud/webhooks`.
The
[`scc-notifications` Terraform submodule](https://github.com/bitwise-media-group/terraform-google-gke-flux/tree/main/modules/scc-notifications)
creates that path: topic, notification config, subscription, dead-letter queue and IAM. It outputs the two values the
`Integration` must match.

Authentication uses the **OIDC token Pub/Sub signs** instead of an HMAC. A push subscription cannot compute an HMAC,
because Pub/Sub composes the message and the sender never sees the bytes patchy receives. The token is also stronger: it
is asymmetric, short-lived, and bound to both a service account and an audience. Patchy checks both. Anyone with a
Google Cloud account can mint a Google-signed token, so only the _identity_ and _audience_ make a token mean something.

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

When patchy declines a delivery, Pub/Sub retries it and then sends it to the dead-letter topic. Nothing is dropped
silently. Set up alerts on two signals:

- anything in the dead-letter topic
- the age of the subscription's oldest unacknowledged message

Patchy answers 503 when its delivery queue is full, and Pub/Sub correctly treats that as backpressure (a signal to slow
down).

## What patchy keeps

The notification is self-contained, so ingest makes no API call. From it a `Finding` gets:

| Finding field        | From                                                            |
| -------------------- | --------------------------------------------------------------- |
| `spec.cloudResource` | `finding.resourceName` plus the notification's `resource` block |
| `spec.advisories`    | the CVE when there is one, then `category:<CATEGORY>`           |
| `spec.alerts[].id`   | `finding.name`, the full SCC resource name                      |
| `spec.severity`      | `finding.severity`, lowercased                                  |
| `spec.description`   | the whole notification, rendered to markdown                    |

Patchy skips a notification when the finding:

- is not `ACTIVE`
- is muted
- is an `SCC_ERROR` (a detector that could not run; that is an operational problem for whoever owns the SCC
  configuration, and there is nothing to triage)
- falls below `minSeverity`

**Accumulation is per resource, not per project.** SCC sends a new notification on every update to a finding, and those
notifications fold into one `Finding`. Two different buckets with the same misconfiguration stay separate. Patchy
resolves the repository per resource, so a family that spans resources could resolve to two different repositories, with
no way to pick the right one.

## What is not implemented

**Muting on an `ignore` verdict.** Patchy dismisses a GHAS alert when it judges a finding to be a false positive. The
SCC equivalent is muting, which needs `securitycenter.findings.setMute`. That is a Google Cloud _write_ permission, and
integration-controller does not hold it on purpose. The write-back interface (`pkg/source.Resolver`) is in place. The
`googleCloud.securityCommandCenter.mute` block is accepted and validated, but patchy does not act on it yet. Adding
muting later means writing one implementation, with no refactor.
